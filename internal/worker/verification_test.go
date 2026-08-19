package worker

import (
	"context"
	"errors"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

// fakeVerificationStore retient ce qui est mis en file après une construction.
type fakeVerificationStore struct {
	enqueued  []store.JobRequest
	enqueueEr error
}

func (f *fakeVerificationStore) Enqueue(_ context.Context, request store.JobRequest) (int64, error) {
	if f.enqueueEr != nil {
		return 0, f.enqueueEr
	}
	f.enqueued = append(f.enqueued, request)
	return int64(len(f.enqueued)), nil
}

func verificationCatalog() *catalog.Catalog {
	return &catalog.Catalog{
		Pipelines: []catalog.Pipeline{
			{
				Kind: "static-tests", QueueClass: "parallel",
				Params: map[string]catalog.Param{
					"revision": {Source: catalog.SourceInput, Required: true},
					"commit":   {Source: catalog.SourceInput, Required: true},
					"repo-url": {Source: catalog.SourceConfig, Key: "forge.mse.repoUrl"},
				},
			},
			{
				Kind: "security-audit", QueueClass: "parallel",
				Params: map[string]catalog.Param{
					// L'audit nomme sa révision autrement que les tests statiques.
					"app-revision": {Source: catalog.SourceInput, Required: true},
					"app-repo-url": {Source: catalog.SourceConfig, Key: "forge.mse.repoUrl"},
				},
			},
			// Présent au catalogue mais non enchaîné : il exige une révision que le
			// dashboard ne connaît pas.
			{
				Kind: "integration-tests", QueueClass: "exclusive-mse-pipeline",
				Params: map[string]catalog.Param{
					"mse-revision":        {Source: catalog.SourceInput, Required: true},
					"mse-kustom-revision": {Source: catalog.SourceInput, Required: true},
				},
			},
		},
	}
}

func sinkFor(store *fakeVerificationStore, pipelines *catalog.Catalog) *runSink {
	sink := &runSink{logger: quietLogger(), catalog: pipelines}
	sink.verification = store
	return sink
}

// Une construction réussie enchaîne les vérifications sur **le commit
// construit**, jamais sur la branche d'où il vient : celle-ci a pu bouger depuis
// le build, et les tests porteraient alors sur autre chose que l'image (B17).
func TestLaVerificationPorteSurLeCommitConstruit(t *testing.T) {
	fake := &fakeVerificationStore{}
	sink := sinkFor(fake, verificationCatalog())

	launched := sink.launchVerification(context.Background(), store.JobReleaseContext{
		ReleaseID: 7, Tag: "3.2.0-rc1",
		GitRef: "main", GitCommit: "abc123def456",
	})

	if launched != 2 {
		t.Fatalf("%d vérifications lancées, attendu 2", launched)
	}

	kinds := map[string]store.JobRequest{}
	for _, job := range fake.enqueued {
		kinds[job.Kind] = job
	}
	if _, ok := kinds["static-tests"]; !ok {
		t.Error("static-tests devrait être lancé")
	}
	if _, ok := kinds["security-audit"]; !ok {
		t.Error("security-audit devrait être lancé")
	}
	// `integration-tests` exige `mse-kustom-revision`, que le dashboard ne connaît
	// pas : le lancer produirait un échec sans rapport avec le code testé.
	if _, ok := kinds["integration-tests"]; ok {
		t.Error("integration-tests ne doit pas être enchaîné automatiquement")
	}

	static := kinds["static-tests"]
	if static.Params["revision"] != "abc123def456" || static.Params["commit"] != "abc123def456" {
		t.Errorf("params de static-tests: %v — attendu le commit, pas la branche", static.Params)
	}

	// L'audit nomme sa révision `app-revision`. Sans ce nom, il retombait sur son
	// défaut et scannait `CNOUS/mse` à `k8s-unstable` — donc autre chose que la
	// release, en rendant un verdict qu'on rapportait à la mauvaise chose.
	audit := kinds["security-audit"]
	if audit.Params["app-revision"] != "abc123def456" {
		t.Errorf("params de security-audit: %v — attendu le commit en app-revision", audit.Params)
	}
	if static.ReleaseID == nil || *static.ReleaseID != 7 {
		t.Error("les vérifications doivent porter leur release, sans quoi leur conclusion ne fait rien avancer")
	}
}

// Sans commit résolu, la vérification retombe sur la référence : moins bon, mais
// mieux que de ne rien lancer.
func TestSansCommitLaVerificationRetombeSurLaReference(t *testing.T) {
	fake := &fakeVerificationStore{}
	sink := sinkFor(fake, verificationCatalog())

	launched := sink.launchVerification(context.Background(), store.JobReleaseContext{
		ReleaseID: 7, Tag: "3.2.0-rc1", GitRef: "main",
	})

	if launched != 2 {
		t.Fatalf("%d vérifications lancées", launched)
	}
	for _, job := range fake.enqueued {
		if job.Kind == "static-tests" && job.Params["revision"] != "main" {
			t.Errorf("révision %q, attendu le repli sur la référence", job.Params["revision"])
		}
	}
}

// Aucune référence du tout : rien à tester, et le dire vaut mieux que d'enfiler
// un run qui échouera au clone.
func TestSansReferenceAucuneVerificationNePart(t *testing.T) {
	fake := &fakeVerificationStore{}
	sink := sinkFor(fake, verificationCatalog())

	if launched := sink.launchVerification(context.Background(),
		store.JobReleaseContext{ReleaseID: 7, Tag: "3.2.0-rc1"}); launched != 0 {
		t.Errorf("%d vérifications lancées, attendu 0", launched)
	}
	if len(fake.enqueued) != 0 {
		t.Errorf("jobs enfilés: %+v", fake.enqueued)
	}
}

// Un échec de mise en file est consigné, pas propagé : la construction a réussi,
// et perdre ce fait parce qu'un test n'a pas pu être enfilé serait pire.
func TestUnEchecDeMiseEnFileNeFaitPasEchouerLaConclusion(t *testing.T) {
	fake := &fakeVerificationStore{enqueueEr: errors.New("file indisponible")}
	sink := sinkFor(fake, verificationCatalog())

	if launched := sink.launchVerification(context.Background(), store.JobReleaseContext{
		ReleaseID: 7, Tag: "3.2.0-rc1", GitCommit: "abc123",
	}); launched != 0 {
		t.Errorf("%d vérifications lancées malgré l'échec", launched)
	}
}

// Sans catalogue branché, rien ne part — et l'observation continue. Un
// observateur qui tomberait faute de catalogue cesserait aussi de projeter les
// runs, ce qui est bien pire que de ne pas enchaîner.
func TestSansCatalogueRienNePart(t *testing.T) {
	fake := &fakeVerificationStore{}
	sink := sinkFor(fake, nil)

	if launched := sink.launchVerification(context.Background(), store.JobReleaseContext{
		ReleaseID: 7, Tag: "3.2.0-rc1", GitCommit: "abc123",
	}); launched != 0 {
		t.Errorf("%d vérifications lancées sans catalogue", launched)
	}
}
