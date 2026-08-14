package catalog

import (
	"context"
	"strings"
	"testing"
)

// Une faute de frappe dans `uploads[].param` ne se manifesterait qu'au premier
// run, rejeté par Tekton pour paramètre inconnu — c'est-à-dire après avoir
// attendu qu'un job soit ordonnancé.
func TestValidateRejectsAnUploadParamThePipelineIgnores(t *testing.T) {
	pipelines := &Catalog{Pipelines: []Pipeline{{
		Kind: "static-tests", PipelineRef: "php-static-tests",
		Uploads: map[string]Upload{
			"results": {Param: "result-upload-url", Artifact: "static-tests.tar.gz"},
		},
	}}}

	err := pipelines.ValidateAgainst(context.Background(), fakeCluster{pipelines: map[string]PipelineSpec{
		"php-static-tests": {Name: "php-static-tests", Params: map[string]bool{
			"results-upload-url": true,
		}},
	}})
	if err == nil {
		t.Fatal("un paramètre d'artefact inconnu doit empêcher le démarrage")
	}
	if !strings.Contains(err.Error(), "result-upload-url") {
		t.Errorf("erreur = %v, attendu le nom du paramètre fautif", err)
	}
}

// Le paramètre d'artefact ne figure pas dans `params` : il est miné au
// lancement. Le compter comme manquant refuserait un catalogue correct.
func TestAnUploadParamCountsAsProvided(t *testing.T) {
	pipelines := &Catalog{Pipelines: []Pipeline{{
		Kind: "static-tests", PipelineRef: "php-static-tests",
		Uploads: map[string]Upload{
			"results": {Param: "results-upload-url", Artifact: "static-tests.tar.gz"},
		},
	}}}

	err := pipelines.ValidateAgainst(context.Background(), fakeCluster{pipelines: map[string]PipelineSpec{
		"php-static-tests": {Name: "php-static-tests", Params: map[string]bool{
			// Sans défaut : le Pipeline l'exige.
			"results-upload-url": false,
		}},
	}})
	if err != nil {
		t.Errorf("erreur = %v, attendu aucune", err)
	}
}

func TestAnIncompleteUploadIsRejected(t *testing.T) {
	pipelines := &Catalog{Pipelines: []Pipeline{{
		Kind: "static-tests", PipelineRef: "php-static-tests",
		Uploads: map[string]Upload{"results": {Param: "results-upload-url"}},
	}}}

	err := pipelines.ValidateAgainst(context.Background(), fakeCluster{pipelines: map[string]PipelineSpec{
		"php-static-tests": {Name: "php-static-tests", Params: map[string]bool{
			"results-upload-url": true,
		}},
	}})
	if err == nil {
		t.Error("un artefact sans nom d'objet ne permet pas de calculer sa clé")
	}
}
