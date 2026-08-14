package worker

import (
	"context"
	"log/slog"
	"net/url"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
)

type fakePresigner struct {
	signed map[string]string
	err    error
}

func (f fakePresigner) PresignPut(_ context.Context, key string, _ time.Duration) (*url.URL, error) {
	if f.err != nil {
		return nil, f.err
	}
	signed := "https://s3.example/" + key + "?X-Amz-Signature=abc"
	f.signed[key] = signed
	return url.Parse(signed)
}

func uploadPipeline() catalog.Pipeline {
	return catalog.Pipeline{
		Kind: "static-tests", PipelineRef: "php-static-tests",
		Uploads: map[string]catalog.Upload{
			"results": {Param: "results-upload-url", Artifact: "static-tests.tar.gz", Family: "static"},
		},
	}
}

// La clé est calculée avant la création du run : c'est ce qui permet au
// dashboard de la retrouver ensuite sans balayer un préfixe (B14).
func TestUploadURLsAreMintedBeforeTheRun(t *testing.T) {
	presigner := fakePresigner{signed: map[string]string{}}
	scheduler := &Scheduler{uploads: presigner, logger: slog.New(slog.DiscardHandler)}

	params := map[string]string{}
	if err := scheduler.addUploadURLs(context.Background(), uploadPipeline(), "static-tests-42", params); err != nil {
		t.Fatalf("URLs: %v", err)
	}

	const key = "raw/static-tests-42/static-tests.tar.gz"
	if _, signed := presigner.signed[key]; !signed {
		t.Errorf("clés signées = %v, attendu %q", presigner.signed, key)
	}
	if params["results-upload-url"] != presigner.signed[key] {
		t.Errorf("paramètre = %q", params["results-upload-url"])
	}
}

// Sans stockage, le run part quand même : le refuser priverait l'utilisateur
// d'une exécution qui reste utile, ses journaux passant par un autre chemin.
func TestWithoutStorageTheRunStillStarts(t *testing.T) {
	scheduler := &Scheduler{logger: slog.New(slog.DiscardHandler)}

	params := map[string]string{}
	if err := scheduler.addUploadURLs(context.Background(), uploadPipeline(), "static-tests-42", params); err != nil {
		t.Fatalf("erreur = %v, attendu aucune", err)
	}
	if len(params) != 0 {
		t.Errorf("paramètres = %v, attendu aucun", params)
	}
}

func TestAnIncompleteUploadDeclarationIsRefused(t *testing.T) {
	scheduler := &Scheduler{
		uploads: fakePresigner{signed: map[string]string{}}, logger: slog.New(slog.DiscardHandler),
	}
	pipeline := catalog.Pipeline{
		Kind:    "static-tests",
		Uploads: map[string]catalog.Upload{"results": {Artifact: "x.tar.gz"}},
	}

	if err := scheduler.addUploadURLs(context.Background(), pipeline, "run-1", map[string]string{}); err == nil {
		t.Error("un artefact sans paramètre cible doit être refusé")
	}
}

// Un tag de release compose des clés ; un nom de run aussi. La validation des
// segments vaut ici comme ailleurs.
func TestAnUnsafeRunNameIsRefused(t *testing.T) {
	scheduler := &Scheduler{
		uploads: fakePresigner{signed: map[string]string{}}, logger: slog.New(slog.DiscardHandler),
	}

	if err := scheduler.addUploadURLs(
		context.Background(), uploadPipeline(), "../evasion", map[string]string{}); err == nil {
		t.Error("un nom de run hors grammaire doit être refusé")
	}
}
