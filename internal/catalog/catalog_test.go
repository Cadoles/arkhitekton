package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const minimal = `
pipelines:
  - kind: static-tests
    pipelineRef: php-static-tests
    label: Tests statiques
    queueClass: parallel
    params:
      revision: { source: input, required: true }
      repo-url: { source: config, key: forge.mse.sshUrl }
`

func TestParseAppliesTheDefaultConcurrencyCap(t *testing.T) {
	catalog, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if catalog.MaxConcurrentRuns != DefaultMaxConcurrentRuns {
		t.Errorf("plafond = %d, attendu %d", catalog.MaxConcurrentRuns, DefaultMaxConcurrentRuns)
	}
	if _, found := catalog.Find("static-tests"); !found {
		t.Error("kind déclaré introuvable")
	}
}

// Une clé mal orthographiée dans un ConfigMap doit faire échouer le chargement.
// Ignorée en silence, elle produirait un run privé du workspace ou du compte de
// service attendu, et l'échec serait attribué au pipeline.
func TestUnknownFieldIsRejected(t *testing.T) {
	_, err := Parse([]byte(`
pipelines:
  - kind: static-tests
    pipelineRef: php-static-tests
    queueClass: parallel
    serviceAccount: tekton-kubectl
`))
	if err == nil {
		t.Fatal("un champ inconnu doit être refusé")
	}
	if !strings.Contains(err.Error(), "serviceAccount") {
		t.Errorf("le message ne désigne pas le champ fautif: %v", err)
	}
}

func TestQueueClassValidation(t *testing.T) {
	valid := []string{"parallel", "exclusive-e2e", "exclusive-mse-pipeline", "exclusive-cluster"}
	invalid := []string{"", "exclusive-", "serial", "Parallel", "exclusive cluster"}

	for _, class := range valid {
		if !ValidQueueClass(class) {
			t.Errorf("classe %q refusée à tort", class)
		}
	}
	for _, class := range invalid {
		if ValidQueueClass(class) {
			t.Errorf("classe %q acceptée à tort", class)
		}
	}
}

func TestDuplicateKindIsRejected(t *testing.T) {
	_, err := Parse([]byte(`
pipelines:
  - kind: build
    pipelineRef: build-image
    queueClass: parallel
  - kind: build
    pipelineRef: build-image-bis
    queueClass: parallel
`))
	if err == nil || !strings.Contains(err.Error(), "deux fois") {
		t.Errorf("un kind dupliqué doit être refusé: %v", err)
	}
}

func TestWorkspaceNeedsExactlyOneSource(t *testing.T) {
	_, err := Parse([]byte(`
pipelines:
  - kind: build
    pipelineRef: build-image
    queueClass: parallel
    workspaces:
      - name: sans-source
`))
	if err == nil {
		t.Fatal("un workspace sans source doit être refusé")
	}

	_, err = Parse([]byte(`
pipelines:
  - kind: build
    pipelineRef: build-image
    queueClass: parallel
    workspaces:
      - name: deux-sources
        emptyDir: true
        secret: { secretName: git-credentials }
`))
	if err == nil {
		t.Error("un workspace à deux sources doit être refusé")
	}
}

func TestParamSourcesAreChecked(t *testing.T) {
	cases := map[string]string{
		"source inconnue":     `revision: { source: magique }`,
		"config sans clé":     `revision: { source: config }`,
		"literal sans valeur": `revision: { source: literal }`,
	}

	for name, params := range cases {
		_, err := Parse([]byte(`
pipelines:
  - kind: build
    pipelineRef: build-image
    queueClass: parallel
    params:
      ` + params + `
`))
		if err == nil {
			t.Errorf("%s: doit être refusé", name)
		}
	}
}

func TestRequiredInputsAreListed(t *testing.T) {
	catalog, err := Parse([]byte(`
pipelines:
  - kind: build
    pipelineRef: build-image
    queueClass: parallel
    params:
      revision: { source: input, required: true }
      commit:   { source: input, required: true }
      branch:   { source: input }
      repo-url: { source: config, key: forge.url }
`))
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}

	pipeline, _ := catalog.Find("build")
	required := pipeline.RequiredInputs()

	if len(required) != 2 || required[0] != "commit" || required[1] != "revision" {
		t.Errorf("entrées requises = %v, attendu [commit revision]", required)
	}
}

// --- Validation contre le cluster --------------------------------------------

type fakeCluster struct {
	pipelines map[string]PipelineSpec
	err       error
}

func (c fakeCluster) Pipelines(context.Context) (map[string]PipelineSpec, error) {
	return c.pipelines, c.err
}

func TestValidateAgainstAcceptsAConsistentCatalog(t *testing.T) {
	catalog, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}

	cluster := fakeCluster{pipelines: map[string]PipelineSpec{
		"php-static-tests": {Name: "php-static-tests", Params: map[string]bool{
			"revision": false,
			"repo-url": false,
			"branch":   true,
		}},
	}}

	if err := catalog.ValidateAgainst(context.Background(), cluster); err != nil {
		t.Errorf("catalogue cohérent refusé: %v", err)
	}
}

// Le Pipeline référencé n'existe pas : le run serait créé puis échouerait en
// `CouldntGetPipeline`, motif qui ne désigne pas le catalogue.
func TestValidateAgainstDetectsMissingPipeline(t *testing.T) {
	catalog, _ := Parse([]byte(minimal))
	cluster := fakeCluster{pipelines: map[string]PipelineSpec{
		"security-audit": {Name: "security-audit"},
	}}

	err := catalog.ValidateAgainst(context.Background(), cluster)
	if err == nil {
		t.Fatal("un Pipeline absent doit être détecté")
	}
	if !strings.Contains(err.Error(), "php-static-tests") {
		t.Errorf("le message ne nomme pas le Pipeline manquant: %v", err)
	}
	// Le message liste ce qui existe : sans cela, diagnostiquer une faute de
	// frappe suppose d'aller interroger le cluster à la main.
	if !strings.Contains(err.Error(), "security-audit") {
		t.Errorf("le message ne liste pas les Pipeline déployés: %v", err)
	}
}

func TestValidateAgainstDetectsUnknownParam(t *testing.T) {
	catalog, _ := Parse([]byte(minimal))
	cluster := fakeCluster{pipelines: map[string]PipelineSpec{
		"php-static-tests": {Name: "php-static-tests", Params: map[string]bool{"revision": false}},
	}}

	err := catalog.ValidateAgainst(context.Background(), cluster)
	if err == nil || !strings.Contains(err.Error(), "repo-url") {
		t.Errorf("un paramètre inconnu du Pipeline doit être détecté: %v", err)
	}
}

// Le cas qui produit `ParameterMissing`, motif qui a déjà piégé un outil de suivi
// de ce projet parce qu'il ne figurait dans aucune liste connue.
func TestValidateAgainstDetectsUncoveredRequiredParam(t *testing.T) {
	catalog, _ := Parse([]byte(minimal))
	cluster := fakeCluster{pipelines: map[string]PipelineSpec{
		"php-static-tests": {Name: "php-static-tests", Params: map[string]bool{
			"revision": false,
			"repo-url": false,
			"commit":   false, // requis par le Pipeline, absent du catalogue
		}},
	}}

	err := catalog.ValidateAgainst(context.Background(), cluster)
	if err == nil || !strings.Contains(err.Error(), "commit") {
		t.Errorf("un paramètre requis non couvert doit être détecté: %v", err)
	}
}

// Un paramètre du Pipeline qui a une valeur par défaut n'a pas à être fourni :
// l'exiger obligerait le catalogue à recopier des valeurs qui vivent déjà dans
// `mse-tekton`, et à les maintenir en double.
func TestValidateAgainstIgnoresDefaultedParams(t *testing.T) {
	catalog, _ := Parse([]byte(minimal))
	cluster := fakeCluster{pipelines: map[string]PipelineSpec{
		"php-static-tests": {Name: "php-static-tests", Params: map[string]bool{
			"revision":    false,
			"repo-url":    false,
			"branch-name": true,
			"version":     true,
		}},
	}}

	if err := catalog.ValidateAgainst(context.Background(), cluster); err != nil {
		t.Errorf("des paramètres à valeur par défaut ont été exigés: %v", err)
	}
}

func TestValidateAgainstReportsClusterFailure(t *testing.T) {
	catalog, _ := Parse([]byte(minimal))
	cluster := fakeCluster{err: errors.New("forbidden")}

	if err := catalog.ValidateAgainst(context.Background(), cluster); err == nil {
		t.Error("une erreur de lecture du cluster doit remonter")
	}
}
