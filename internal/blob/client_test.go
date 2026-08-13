package blob

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/config"
)

// Ces tests exigent un MinIO réel : ce que l'on veut vérifier — la signature
// SigV4, la lecture par plage, l'écriture via URL pré-signée — n'a de sens que
// contre un serveur qui les implémente.
//
// Sans TEST_MINIO_ENDPOINT, les tests sont ignorés visiblement.
func testStorage(t *testing.T) config.StorageConfig {
	t.Helper()

	endpoint := os.Getenv("TEST_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_MINIO_ENDPOINT non défini: test d'intégration ignoré")
	}
	return config.StorageConfig{
		Endpoint:   endpoint,
		Bucket:     envOr("TEST_MINIO_BUCKET", "mse-dashboard-test"),
		AccessKey:  envOr("TEST_MINIO_ACCESS_KEY", "minioadmin"),
		SecretKey:  envOr("TEST_MINIO_SECRET_KEY", "minioadmin"),
		Region:     envOr("TEST_MINIO_REGION", "us-east-1"),
		PresignTTL: 2 * time.Hour,
		UseSSL:     os.Getenv("TEST_MINIO_SSL") == "true",
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func testClient(t *testing.T) *Client {
	t.Helper()

	client, err := Open(context.Background(), testStorage(t))
	if err != nil {
		t.Fatalf("ouverture: %v (le bucket existe-t-il ?)", err)
	}
	return client
}

// Un bucket absent doit faire échouer le démarrage. Sans cette vérification, le
// client minio-go n'ouvrant aucune connexion à la construction, la panne
// n'apparaîtrait qu'à la première publication d'artefact — soit à la fin d'un
// pipeline, après le travail coûteux.
func TestOpenRejectsMissingBucket(t *testing.T) {
	cfg := testStorage(t)
	cfg.Bucket = "bucket-qui-nexiste-pas-" + time.Now().Format("20060102150405")

	if _, err := Open(context.Background(), cfg); err == nil {
		t.Fatal("un bucket absent doit faire échouer Open")
	}
}

func TestPutGetRemove(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	key, err := client.Keys.RawArtifact("run-test-putget", "sonde.txt")
	if err != nil {
		t.Fatalf("clé: %v", err)
	}
	content := []byte("contenu de sonde")

	if err := client.Put(ctx, key, bytes.NewReader(content), int64(len(content)), "text/plain"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	t.Cleanup(func() { _ = client.Remove(ctx, key) })

	reader, err := client.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer reader.Close()

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("contenu = %q, attendu %q", got, content)
	}
}

// GetObject est paresseux : sans le Stat de Get, l'erreur d'objet absent
// n'apparaîtrait qu'à la première lecture, loin de l'appel fautif.
func TestGetReportsMissingObjectImmediately(t *testing.T) {
	client := testClient(t)

	if _, err := client.Get(context.Background(), "raw/run-absent/rien.txt"); err == nil {
		t.Fatal("un objet absent doit faire échouer Get")
	}
}

// Les logs d'un pipeline d'intégration pèsent des dizaines de mégaoctets, et
// l'interface n'en affiche que la fin : charger l'objet entier serait ruineux.
func TestGetRangeReadsOnlyRequestedBytes(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	key, err := client.Keys.TaskLog("run-test-range", "task", "step-0")
	if err != nil {
		t.Fatalf("clé: %v", err)
	}
	content := []byte("0123456789abcdefghij")

	if err := client.Put(ctx, key, bytes.NewReader(content), int64(len(content)), "text/plain"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	t.Cleanup(func() { _ = client.Remove(ctx, key) })

	reader, err := client.GetRange(ctx, key, 10, 5)
	if err != nil {
		t.Fatalf("GetRange: %v", err)
	}
	defer reader.Close()

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if string(got) != "abcde" {
		t.Errorf("plage lue = %q, attendu %q", got, "abcde")
	}
}

// Le cœur de la publication : le pipeline reçoit une URL et écrit sans détenir
// le moindre identifiant. Le test refait exactement ce que fait la task —
// un PUT sur l'URL, rien d'autre.
func TestPresignedPutAcceptsUploadWithoutCredentials(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	key, err := client.Keys.RawArtifact("run-test-presign", "junit.xml")
	if err != nil {
		t.Fatalf("clé: %v", err)
	}

	presigned, err := client.PresignPut(ctx, key, 10*time.Minute)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	t.Cleanup(func() { _ = client.Remove(ctx, key) })

	// La signature doit être portée par la requête elle-même : aucun en-tête
	// d'authentification n'est ajouté ici, comme dans la task.
	body := "<testsuites/>"
	request, err := newPutRequest(ctx, presigned.String(), body)
	if err != nil {
		t.Fatalf("requête: %v", err)
	}
	response, err := httpDo(request)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != 200 {
		detail, _ := io.ReadAll(response.Body)
		t.Fatalf("PUT = %d, attendu 200: %s", response.StatusCode, detail)
	}

	reader, err := client.Get(ctx, key)
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}
	defer reader.Close()

	got, _ := io.ReadAll(reader)
	if string(got) != body {
		t.Errorf("contenu relu = %q, attendu %q", got, body)
	}
}

// L'URL doit porter une signature SigV4 en query string : c'est cette forme que
// `curl -T` consomme, et non un formulaire POST signé.
func TestPresignedURLCarriesSigV4QueryParameters(t *testing.T) {
	client := testClient(t)

	presigned, err := client.PresignPut(context.Background(), "raw/run/objet.txt", time.Hour)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}

	query := presigned.Query()
	for _, param := range []string{
		"X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Date",
		"X-Amz-Expires", "X-Amz-SignedHeaders", "X-Amz-Signature",
	} {
		if query.Get(param) == "" {
			t.Errorf("paramètre %s absent de l'URL pré-signée", param)
		}
	}
	if algo := query.Get("X-Amz-Algorithm"); algo != "AWS4-HMAC-SHA256" {
		t.Errorf("algorithme = %q, attendu AWS4-HMAC-SHA256", algo)
	}
}

// Une URL expirée doit être refusée par le serveur : c'est ce qui borne la
// portée de la capacité d'écriture confiée au pipeline.
func TestPresignedPutExpires(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	presigned, err := client.PresignPut(ctx, "raw/run-test-expire/objet.txt", time.Second)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}

	time.Sleep(2 * time.Second)

	request, err := newPutRequest(ctx, presigned.String(), "trop tard")
	if err != nil {
		t.Fatalf("requête: %v", err)
	}
	response, err := httpDo(request)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode == 200 {
		t.Error("une URL expirée a été acceptée")
	}
}

func TestListReturnsObjectsUnderPrefix(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	prefix, err := client.Keys.RunPrefix("run-test-list")
	if err != nil {
		t.Fatalf("préfixe: %v", err)
	}

	for _, name := range []string{"a.xml", "b.json"} {
		key, err := client.Keys.RawArtifact("run-test-list", name)
		if err != nil {
			t.Fatalf("clé: %v", err)
		}
		content := strings.NewReader("x")
		if err := client.Put(ctx, key, content, 1, "text/plain"); err != nil {
			t.Fatalf("Put: %v", err)
		}
		t.Cleanup(func() { _ = client.Remove(ctx, key) })
	}

	objects, err := client.List(ctx, prefix)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objects) != 2 {
		t.Errorf("%d objet(s) listé(s), attendu 2: %+v", len(objects), objects)
	}
}
