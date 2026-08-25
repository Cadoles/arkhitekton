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
    serviceAccount: tekton-kubectl
`))
	if err == nil {
		t.Fatal("un champ inconnu doit être refusé")
	}
	if !strings.Contains(err.Error(), "serviceAccount") {
		t.Errorf("le message ne désigne pas le champ fautif: %v", err)
	}
}

// Le plafond d'un type : déclaré, ou celui du défaut. Un type nouveau démarre
// parallélisable — le cas courant — et s'exclut en le déclarant plutôt qu'en
// l'oubliant.
func TestLePlafondDunTypeRetombeSurLeDefaut(t *testing.T) {
	if got := (Pipeline{Kind: "unit-tests"}).Concurrency(); got != DefaultKindConcurrency {
		t.Errorf("plafond = %d, attendu le défaut %d", got, DefaultKindConcurrency)
	}
	if got := (Pipeline{Kind: "e2e-tests", MaxConcurrent: 1}).Concurrency(); got != 1 {
		t.Errorf("plafond déclaré = %d, attendu 1", got)
	}
}

func TestDuplicateKindIsRejected(t *testing.T) {
	_, err := Parse([]byte(`
pipelines:
  - kind: build
    pipelineRef: build-image
  - kind: build
    pipelineRef: build-image-bis
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

// Le cas constaté sur le cluster : le catalogue passait la validation des
// paramètres, démarrait, puis produisait des runs morts en
// `InvalidWorkspaceBindings` à la seconde. La validation doit donc couvrir les
// workspaces comme les paramètres.
func TestValidateAgainstDetectsUnboundWorkspace(t *testing.T) {
	catalog, _ := Parse([]byte(minimal))
	cluster := fakeCluster{pipelines: map[string]PipelineSpec{
		"php-static-tests": {
			Name:   "php-static-tests",
			Params: map[string]bool{"revision": false, "repo-url": false},
			Workspaces: map[string]bool{
				"shared-data":     false,
				"git-credentials": false,
			},
		},
	}}

	err := catalog.ValidateAgainst(context.Background(), cluster)
	if err == nil {
		t.Fatal("un workspace non lié doit être détecté")
	}
	for _, name := range []string{"shared-data", "git-credentials"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("le message ne nomme pas le workspace %q: %v", name, err)
		}
	}
}

// Un workspace lié sous un nom que le Pipeline ne connaît pas est tout aussi
// fatal, et le message doit lister les noms attendus : `security-audit` emploie
// `shared` et `docker-config` là où les autres pipelines emploient `shared-data`
// et `dockerconfig`, et rien ne le devine.
func TestValidateAgainstDetectsMisnamedWorkspace(t *testing.T) {
	catalog, err := Parse([]byte(`
pipelines:
  - kind: audit
    pipelineRef: security-audit
    workspaces:
      - name: shared-data
        emptyDir: true
`))
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}

	cluster := fakeCluster{pipelines: map[string]PipelineSpec{
		"security-audit": {
			Name:       "security-audit",
			Workspaces: map[string]bool{"shared": false},
		},
	}}

	err = catalog.ValidateAgainst(context.Background(), cluster)
	if err == nil || !strings.Contains(err.Error(), "shared") {
		t.Errorf("un workspace mal nommé doit être détecté: %v", err)
	}
}

// Un workspace optionnel n'a pas à être lié.
func TestValidateAgainstIgnoresOptionalWorkspaces(t *testing.T) {
	catalog, _ := Parse([]byte(minimal))
	cluster := fakeCluster{pipelines: map[string]PipelineSpec{
		"php-static-tests": {
			Name:       "php-static-tests",
			Params:     map[string]bool{"revision": false, "repo-url": false},
			Workspaces: map[string]bool{"facultatif": true},
		},
	}}

	if err := catalog.ValidateAgainst(context.Background(), cluster); err != nil {
		t.Errorf("un workspace optionnel a été exigé: %v", err)
	}
}

// Une vérification sans artefact tourne, conclut, et son verdict n'atteint jamais
// le dashboard : la release reste en attente d'un résultat que rien ne remonte.
// Refusé au démarrage plutôt que constaté au bout d'une campagne.
func TestUneVerificationSansArtefactEstRefusee(t *testing.T) {
	_, err := Parse([]byte(`
pipelines:
  - kind: unit-tests
    pipelineRef: php-unit-tests
    verification: true
`))
	if err == nil {
		t.Fatal("une vérification sans artefact doit être refusée")
	}
	if !strings.Contains(err.Error(), "au moins un artefact") {
		t.Errorf("message = %v", err)
	}
}

// Et le pendant : un pipeline non enchaîné n'a rien à publier. `build` produit une
// image, pas un rapport, et lui réclamer un artefact le rendrait indéclarable.
func TestUnPipelineNonEnchaineNaPasBesoinDArtefact(t *testing.T) {
	if _, err := Parse([]byte(`
pipelines:
  - kind: build
    pipelineRef: build-image
`)); err != nil {
		t.Fatalf("un pipeline ordinaire sans artefact doit être accepté: %v", err)
	}
}
