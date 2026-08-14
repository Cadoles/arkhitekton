package logs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/blob"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/config"
)

// Ces tests exigent un MinIO réel. Ce qu'ils vérifient — découpage en tranches
// durables, ordre du scellement, reprise après interruption — porte sur ce qui est
// effectivement écrit dans le stockage objet, et un bouchon en mémoire ne
// prouverait que la cohérence du bouchon.
func testStorage(t *testing.T) *blob.Client {
	t.Helper()

	endpoint := os.Getenv("TEST_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_MINIO_ENDPOINT non défini: test d'intégration ignoré")
	}

	client, err := blob.Open(context.Background(), config.StorageConfig{
		Endpoint:   endpoint,
		Bucket:     envOr("TEST_MINIO_BUCKET", "mse-dashboard-test"),
		AccessKey:  envOr("TEST_MINIO_ACCESS_KEY", "minioadmin"),
		SecretKey:  envOr("TEST_MINIO_SECRET_KEY", "minioadmin"),
		Region:     envOr("TEST_MINIO_REGION", "us-east-1"),
		PresignTTL: 2 * time.Hour,
	})
	if err != nil {
		t.Fatalf("ouverture du stockage: %v", err)
	}
	return client
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakePods rend, par conteneur, un contenu figé puis EOF — ce que fait un
// conteneur terminé.
type fakePods struct {
	mu       sync.Mutex
	byName   map[string]string
	requests []request
	// missing simule un conteneur pas encore démarré : la lecture échoue.
	missing map[string]bool
}

type request struct {
	container string
	since     *time.Time
}

func newFakePods(byName map[string]string) *fakePods {
	return &fakePods{byName: byName, missing: map[string]bool{}}
}

func (p *fakePods) Stream(_ context.Context, _, container string, since *time.Time) (io.ReadCloser, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.requests = append(p.requests, request{container: container, since: since})

	if p.missing[container] {
		return nil, fmt.Errorf("conteneur %q en attente de démarrage", container)
	}

	content, ok := p.byName[container]
	if !ok {
		return nil, fmt.Errorf("conteneur %q inconnu", container)
	}

	// Filtrage à la seconde, comme le fait l'API Kubernetes : c'est ce qui rend
	// les doublons inévitables après une reprise.
	if since != nil {
		var kept []string
		for _, line := range strings.SplitAfter(content, "\n") {
			if line == "" {
				continue
			}
			parsed := parseLine(line)
			if parsed.Timestamp.IsZero() || !parsed.Timestamp.Truncate(time.Second).Before(since.Truncate(time.Second)) {
				kept = append(kept, line)
			}
		}
		content = strings.Join(kept, "")
	}

	return io.NopCloser(strings.NewReader(content)), nil
}

func (p *fakePods) sinceFor(container string) []*time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()

	var values []*time.Time
	for _, r := range p.requests {
		if r.container == container {
			values = append(values, r.since)
		}
	}
	return values
}

type fakeStore struct {
	mu      sync.Mutex
	sealed  map[string]string
	sealErr error
}

func newFakeStore() *fakeStore { return &fakeStore{sealed: map[string]string{}} }

func (s *fakeStore) SealTaskLog(_ context.Context, runName, taskName, object string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sealErr != nil {
		return s.sealErr
	}
	s.sealed[runName+"/"+taskName] = object
	return nil
}

type recordingSink struct {
	mu    sync.Mutex
	lines []Line
}

func (s *recordingSink) Publish(_, _ string, line Line) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, line)
}

func (s *recordingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.lines)
}

// stamp fabrique une ligne au format que produit kubelet avec `timestamps=true`.
func stamp(offset time.Duration, text string) string {
	base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	return base.Add(offset).Format(time.RFC3339Nano) + " " + text + "\n"
}

func uniqueTarget(t *testing.T, steps ...Step) Target {
	t.Helper()
	// Un nom de run unique par test : le bucket est partagé, et deux tests qui
	// écriraient sous la même clé se masqueraient l'un l'autre.
	return Target{
		RunName:  fmt.Sprintf("run-%d", time.Now().UnixNano()),
		TaskName: "unit-test",
		PodName:  "unit-test-pod",
		Steps:    steps,
	}
}

func cleanup(t *testing.T, storage *blob.Client, target Target) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		prefix, err := storage.Keys.RunPrefix(target.RunName)
		if err == nil {
			_ = prefix
		}
		for _, p := range []string{"logs/" + target.RunName + "/"} {
			objects, err := storage.List(ctx, p)
			if err != nil {
				return
			}
			for _, object := range objects {
				_ = storage.Remove(ctx, object.Key)
			}
		}
	})
}

func newCollector(storage *blob.Client, pods Pods, store Store, sink Sink) *Collector {
	collector := New(pods, storage, store, sink, discardLogger())
	// Seuils resserrés : les valeurs de production — 5 s et 1 Mio — rendraient
	// les tests lents ou incapables de produire plus d'une tranche.
	collector.flushInterval = 50 * time.Millisecond
	collector.flushSize = 120
	return collector
}

// Le scellement doit rendre un journal dont l'ordre est celui de l'exécution.
// Trier sur les clés seules mélangerait les étapes par ordre alphabétique, et un
// journal où `publish` précède `phpunit` ne se lit pas.
func TestSealConcatenatesStepsInDeclaredOrder(t *testing.T) {
	storage := testStorage(t)
	ctx := context.Background()

	target := uniqueTarget(t,
		Step{Name: "composer", Container: "step-composer"},
		Step{Name: "phpunit", Container: "step-phpunit"},
		Step{Name: "publish", Container: "step-publish"},
	)
	cleanup(t, storage, target)

	pods := newFakePods(map[string]string{
		// L'ordre alphabétique des conteneurs est composer, phpunit, publish ;
		// l'ordre déclaré est le même, mais les contenus permettent de vérifier
		// que la concaténation suit bien les étapes et non les clés.
		"step-composer": stamp(0, "composer install"),
		"step-phpunit":  stamp(time.Second, "phpunit ok"),
		"step-publish":  stamp(2*time.Second, "publication"),
	})
	store := newFakeStore()
	collector := newCollector(storage, pods, store, nil)

	collector.Start(ctx, target)
	if err := collector.Finish(ctx, target); err != nil {
		t.Fatalf("scellement: %v", err)
	}

	object, err := storage.Keys.TaskLogObject(target.RunName, target.TaskName)
	if err != nil {
		t.Fatalf("clé: %v", err)
	}
	content := readObject(t, storage, object)

	positions := []string{"composer install", "phpunit ok", "publication"}
	previous := -1
	for _, text := range positions {
		index := strings.Index(content, text)
		if index < 0 {
			t.Fatalf("%q absent du journal scellé:\n%s", text, content)
		}
		if index < previous {
			t.Errorf("%q apparaît avant l'étape précédente", text)
		}
		previous = index
	}

	if store.sealed[target.RunName+"/"+target.TaskName] != object {
		t.Errorf("la base n'a pas été mise à jour: %v", store.sealed)
	}
}

// Les tranches sont purgées après scellement, mais seulement après : l'inverse
// perdrait le journal si l'écriture de l'objet échouait.
func TestSealPurgesChunksButKeepsTheSealedObject(t *testing.T) {
	storage := testStorage(t)
	ctx := context.Background()

	target := uniqueTarget(t, Step{Name: "phpunit", Container: "step-phpunit"})
	cleanup(t, storage, target)

	// Assez de contenu pour produire plusieurs tranches avec un seuil de 120 o.
	var content strings.Builder
	for i := range 20 {
		content.WriteString(stamp(time.Duration(i)*time.Second, fmt.Sprintf("ligne %d", i)))
	}

	pods := newFakePods(map[string]string{"step-phpunit": content.String()})
	collector := newCollector(storage, pods, newFakeStore(), nil)

	collector.Start(ctx, target)

	partsPrefix, err := storage.Keys.TaskLogPartsPrefix(target.RunName, target.TaskName)
	if err != nil {
		t.Fatalf("préfixe: %v", err)
	}
	// Attendre que la capture ait produit des tranches avant de sceller, sinon le
	// test ne prouve rien sur la purge.
	waitFor(t, func() bool {
		objects, err := storage.List(ctx, partsPrefix)
		return err == nil && len(objects) > 1
	}, "plusieurs tranches écrites")

	if err := collector.Finish(ctx, target); err != nil {
		t.Fatalf("scellement: %v", err)
	}

	remaining, err := storage.List(ctx, partsPrefix)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("%d tranche(s) subsistent après scellement", len(remaining))
	}

	object, _ := storage.Keys.TaskLogObject(target.RunName, target.TaskName)
	sealed := readObject(t, storage, object)
	if !strings.Contains(sealed, "ligne 0") || !strings.Contains(sealed, "ligne 19") {
		t.Errorf("journal scellé incomplet (%d octets)", len(sealed))
	}
}

// La reprise après redémarrage du worker : les tranches déjà écrites bornent la
// demande au kubelet, et les lignes renvoyées en double sont écartées.
//
// C'est le comportement qui distingue « perdre des lignes » de « en dupliquer », et
// le document tranche explicitement en faveur du second.
func TestResumeSkipsAlreadyStoredLinesWithoutLosingAny(t *testing.T) {
	storage := testStorage(t)
	ctx := context.Background()

	target := uniqueTarget(t, Step{Name: "phpunit", Container: "step-phpunit"})
	cleanup(t, storage, target)

	full := stamp(0, "ligne A") + stamp(time.Second, "ligne B") + stamp(2*time.Second, "ligne C")
	pods := newFakePods(map[string]string{"step-phpunit": full})

	// Première capture, interrompue : seules les deux premières lignes existent.
	partial := stamp(0, "ligne A") + stamp(time.Second, "ligne B")
	chunkKey, err := storage.Keys.TaskLogChunk(target.RunName, target.TaskName, "step-phpunit", 0, 1)
	if err != nil {
		t.Fatalf("clé: %v", err)
	}
	if err := storage.Put(ctx, chunkKey, strings.NewReader(partial), int64(len(partial)), "text/plain"); err != nil {
		t.Fatalf("tranche initiale: %v", err)
	}

	collector := newCollector(storage, pods, newFakeStore(), nil)
	collector.Start(ctx, target)
	if err := collector.Finish(ctx, target); err != nil {
		t.Fatalf("scellement: %v", err)
	}

	// Le kubelet a bien été interrogé avec une borne : sans elle, tout le journal
	// serait relu à chaque redémarrage.
	sinces := pods.sinceFor("step-phpunit")
	if len(sinces) == 0 || sinces[0] == nil {
		t.Fatalf("la reprise n'a pas positionné de borne: %v", sinces)
	}

	object, _ := storage.Keys.TaskLogObject(target.RunName, target.TaskName)
	sealed := readObject(t, storage, object)

	for _, text := range []string{"ligne A", "ligne B", "ligne C"} {
		if strings.Count(sealed, text) == 0 {
			t.Errorf("%q perdue au scellement:\n%s", text, sealed)
		}
		if strings.Count(sealed, text) > 1 {
			t.Errorf("%q dupliquée %d fois:\n%s", text, strings.Count(sealed, text), sealed)
		}
	}
}

// Une étape pas encore démarrée fait échouer la lecture de son conteneur. Ce n'est
// pas une anomalie : les étapes s'exécutent en série, et la dernière peut attendre
// plusieurs minutes. La capture doit réessayer, pas abandonner.
func TestStepNotStartedYetIsRetried(t *testing.T) {
	storage := testStorage(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	target := uniqueTarget(t, Step{Name: "tardive", Container: "step-tardive"})
	cleanup(t, storage, target)

	pods := newFakePods(map[string]string{"step-tardive": stamp(0, "enfin démarrée")})
	pods.missing["step-tardive"] = true

	collector := newCollector(storage, pods, newFakeStore(), nil)
	collector.flushInterval = 20 * time.Millisecond
	collector.Start(ctx, target)

	waitFor(t, func() bool { return len(pods.sinceFor("step-tardive")) >= 2 }, "au moins deux tentatives")

	// L'étape démarre : la capture doit alors aboutir.
	pods.mu.Lock()
	pods.missing["step-tardive"] = false
	pods.mu.Unlock()

	partsPrefix, _ := storage.Keys.TaskLogPartsPrefix(target.RunName, target.TaskName)
	waitFor(t, func() bool {
		objects, err := storage.List(ctx, partsPrefix)
		return err == nil && len(objects) > 0
	}, "journal capté après démarrage")

	collector.Stop()
}

// L'informer répète l'événement de mise à jour à chaque changement d'étape. Une
// capture par événement ouvrirait des dizaines de flux sur le même conteneur.
func TestStartIsIdempotent(t *testing.T) {
	storage := testStorage(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	target := uniqueTarget(t, Step{Name: "phpunit", Container: "step-phpunit"})
	cleanup(t, storage, target)

	// Conteneur absent : la capture boucle, ce qui laisse le temps d'observer les
	// démarrages concurrents.
	pods := newFakePods(map[string]string{})
	pods.missing["step-phpunit"] = true

	collector := newCollector(storage, pods, newFakeStore(), nil)

	for range 5 {
		collector.Start(ctx, target)
	}

	collector.mu.Lock()
	active := len(collector.active)
	collector.mu.Unlock()

	if active != 1 {
		t.Errorf("%d capture(s) active(s), attendu 1", active)
	}

	collector.Stop()
}

// Sceller une task sans journal — pipeline sans étape, ou task déjà scellée — ne
// doit pas produire d'erreur : les deux cas sont normaux.
func TestSealWithoutChunksIsNotAnError(t *testing.T) {
	storage := testStorage(t)
	target := uniqueTarget(t, Step{Name: "phpunit", Container: "step-phpunit"})

	collector := newCollector(storage, newFakePods(map[string]string{}), newFakeStore(), nil)
	if err := collector.Finish(context.Background(), target); err != nil {
		t.Errorf("scellement à vide: %v", err)
	}
}

// Le direct : chaque ligne captée part aussi vers les abonnés, sans horodatage —
// il serait du bruit à l'écran, alors qu'il est indispensable dans la tranche
// stockée pour la reprise.
func TestLinesArePublishedLiveWithoutTimestampPrefix(t *testing.T) {
	storage := testStorage(t)
	ctx := context.Background()

	target := uniqueTarget(t, Step{Name: "phpunit", Container: "step-phpunit"})
	cleanup(t, storage, target)

	pods := newFakePods(map[string]string{
		"step-phpunit": stamp(0, "PHPUnit 11.4.1") + stamp(time.Second, "OK (1301 tests)"),
	})
	sink := &recordingSink{}
	collector := newCollector(storage, pods, newFakeStore(), sink)

	collector.Start(ctx, target)
	if err := collector.Finish(ctx, target); err != nil {
		t.Fatalf("scellement: %v", err)
	}

	if sink.count() != 2 {
		t.Fatalf("%d ligne(s) publiée(s), attendu 2", sink.count())
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, line := range sink.lines {
		if strings.HasPrefix(line.Text, "2026-") {
			t.Errorf("l'horodatage n'a pas été retiré du direct: %q", line.Text)
		}
		if line.Timestamp.IsZero() {
			t.Errorf("horodatage perdu pour %q", line.Text)
		}
	}

	// Le stockage, lui, conserve l'horodatage.
	object, _ := storage.Keys.TaskLogObject(target.RunName, target.TaskName)
	if sealed := readObject(t, storage, object); !strings.Contains(sealed, "2026-08-14T10:00:00Z") {
		t.Errorf("l'horodatage manque dans le journal stocké:\n%s", sealed)
	}
}

// Une ligne sans horodatage exploitable doit être conservée telle quelle : la
// perdre pour un défaut de format serait le pire des arbitrages.
func TestUnstampedLineIsKept(t *testing.T) {
	line := parseLine("une ligne sans horodatage\n")

	if line.Text != "une ligne sans horodatage" {
		t.Errorf("texte = %q", line.Text)
	}
	if !line.Timestamp.IsZero() {
		t.Errorf("horodatage inventé: %v", line.Timestamp)
	}
}

func TestParseLineSeparatesTimestampFromText(t *testing.T) {
	line := parseLine("2026-08-14T10:00:00.123456789Z PHPUnit 11.4.1\n")

	if line.Text != "PHPUnit 11.4.1" {
		t.Errorf("texte = %q", line.Text)
	}
	if line.Timestamp.IsZero() || line.Timestamp.Year() != 2026 {
		t.Errorf("horodatage = %v", line.Timestamp)
	}
}

func readObject(t *testing.T, storage *blob.Client, key string) string {
	t.Helper()

	reader, err := storage.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("lecture de %q: %v", key, err)
	}
	defer reader.Close()

	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("lecture de %q: %v", key, err)
	}
	return string(content)
}

func waitFor(t *testing.T, condition func() bool, what string) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition jamais atteinte: %s", what)
}
