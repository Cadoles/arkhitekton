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
    produces: [none]
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
		t.Errorf("ceiling = %d, expected %d", catalog.MaxConcurrentRuns, DefaultMaxConcurrentRuns)
	}
	if _, found := catalog.Find("static-tests"); !found {
		t.Error("declared kind not found")
	}
}

// A misspelled key in a ConfigMap must fail the load. Ignored silently, it would
// produce a run missing the workspace or the service account it expects, and the
// failure would be blamed on the pipeline.
func TestUnknownFieldIsRejected(t *testing.T) {
	_, err := Parse([]byte(`
pipelines:
  - kind: static-tests
    produces: [none]
    pipelineRef: php-static-tests
    serviceAccount: tekton-kubectl
`))
	if err == nil {
		t.Fatal("an unknown field must be refused")
	}
	if !strings.Contains(err.Error(), "serviceAccount") {
		t.Errorf("the message does not name the offending field: %v", err)
	}
}

// A kind's ceiling: declared, or the default. A new kind starts parallelisable —
// the common case — and excludes itself by declaring it rather than by forgetting.
func TestAKindCeilingFallsBackToTheDefault(t *testing.T) {
	if got := (Pipeline{Kind: "unit-tests"}).Concurrency(); got != DefaultKindConcurrency {
		t.Errorf("ceiling = %d, expected the default %d", got, DefaultKindConcurrency)
	}
	if got := (Pipeline{Kind: "e2e-tests", MaxConcurrent: 1}).Concurrency(); got != 1 {
		t.Errorf("declared ceiling = %d, expected 1", got)
	}
}

func TestDuplicateKindIsRejected(t *testing.T) {
	_, err := Parse([]byte(`
pipelines:
  - kind: build
    produces: [image]
    pipelineRef: build-image
  - kind: build
    produces: [image]
    pipelineRef: build-image-bis
`))
	if err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Errorf("a duplicate kind must be refused: %v", err)
	}
}

func TestWorkspaceNeedsExactlyOneSource(t *testing.T) {
	_, err := Parse([]byte(`
pipelines:
  - kind: build
    produces: [image]
    pipelineRef: build-image
    workspaces:
      - name: sans-source
`))
	if err == nil {
		t.Fatal("a workspace with no source must be refused")
	}

	_, err = Parse([]byte(`
pipelines:
  - kind: build
    produces: [image]
    pipelineRef: build-image
    workspaces:
      - name: deux-sources
        emptyDir: true
        secret: { secretName: git-credentials }
`))
	if err == nil {
		t.Error("a workspace with two sources must be refused")
	}
}

func TestParamSourcesAreChecked(t *testing.T) {
	cases := map[string]string{
		"unknown source":          `revision: { source: magique }`,
		"config without a key":    `revision: { source: config }`,
		"literal without a value": `revision: { source: literal }`,
	}

	for name, params := range cases {
		_, err := Parse([]byte(`
pipelines:
  - kind: build
    produces: [image]
    pipelineRef: build-image
    params:
      ` + params + `
`))
		if err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

func TestRequiredInputsAreListed(t *testing.T) {
	catalog, err := Parse([]byte(`
pipelines:
  - kind: build
    produces: [image]
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
		t.Errorf("required inputs = %v, expected [commit revision]", required)
	}
}

// --- Validation against the cluster ------------------------------------------

type fakeCluster struct {
	pipelines map[string]PipelineSpec
	tasks     map[string]bool
	err       error
}

func (c fakeCluster) Pipelines(context.Context) (map[string]PipelineSpec, error) {
	return c.pipelines, c.err
}

func (c fakeCluster) Tasks(context.Context) (map[string]bool, error) {
	return c.tasks, c.err
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
		t.Errorf("a consistent catalogue was refused: %v", err)
	}
}

// The referenced Pipeline does not exist: the run would be created and then fail
// with CouldntGetPipeline, a reason that does not name the catalogue.
func TestValidateAgainstDetectsMissingPipeline(t *testing.T) {
	catalog, _ := Parse([]byte(minimal))
	cluster := fakeCluster{pipelines: map[string]PipelineSpec{
		"security-audit": {Name: "security-audit"},
	}}

	err := catalog.ValidateAgainst(context.Background(), cluster)
	if err == nil {
		t.Fatal("a missing Pipeline must be detected")
	}
	if !strings.Contains(err.Error(), "php-static-tests") {
		t.Errorf("the message does not name the missing Pipeline: %v", err)
	}
	// The message lists what does exist: without it, diagnosing a typo means querying
	// the cluster by hand.
	if !strings.Contains(err.Error(), "security-audit") {
		t.Errorf("the message does not list the deployed Pipelines: %v", err)
	}
}

func TestValidateAgainstDetectsUnknownParam(t *testing.T) {
	catalog, _ := Parse([]byte(minimal))
	cluster := fakeCluster{pipelines: map[string]PipelineSpec{
		"php-static-tests": {Name: "php-static-tests", Params: map[string]bool{"revision": false}},
	}}

	err := catalog.ValidateAgainst(context.Background(), cluster)
	if err == nil || !strings.Contains(err.Error(), "repo-url") {
		t.Errorf("a parameter unknown to the Pipeline must be detected: %v", err)
	}
}

// The case that produces ParameterMissing.
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
		t.Errorf("an uncovered required parameter must be detected: %v", err)
	}
}

// A Pipeline parameter that has a default need not be supplied: requiring it would
// force the catalogue to copy values that already live in the Pipeline, and to
// maintain them twice.
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
		t.Errorf("parameters with defaults were demanded: %v", err)
	}
}

func TestValidateAgainstReportsClusterFailure(t *testing.T) {
	catalog, _ := Parse([]byte(minimal))
	cluster := fakeCluster{err: errors.New("forbidden")}

	if err := catalog.ValidateAgainst(context.Background(), cluster); err == nil {
		t.Error("an error reading the cluster must surface")
	}
}

// The case observed on the cluster: the catalogue passed parameter validation,
// started, then produced runs that died with InvalidWorkspaceBindings within a
// second. Validation must therefore cover workspaces as well as parameters.
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
		t.Fatal("an unbound workspace must be detected")
	}
	for _, name := range []string{"shared-data", "git-credentials"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the message does not name workspace %q: %v", name, err)
		}
	}
}

// A workspace bound under a name the Pipeline does not know is just as fatal, and
// the message must list the expected names: one pipeline uses `shared` and
// `docker-config` where the others use `shared-data` and `dockerconfig`, and
// nothing guesses that.
func TestValidateAgainstDetectsMisnamedWorkspace(t *testing.T) {
	catalog, err := Parse([]byte(`
pipelines:
  - kind: audit
    produces: [none]
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
		t.Errorf("a misnamed workspace must be detected: %v", err)
	}
}

// An optional workspace need not be bound.
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
		t.Errorf("an optional workspace was demanded: %v", err)
	}
}

// A verification with no artifact runs, concludes, and its verdict never reaches
// the dashboard: the release keeps waiting for a result nothing reports.
func TestAVerificationWithoutAnArtifactIsRefused(t *testing.T) {
	_, err := Parse([]byte(`
pipelines:
  - kind: unit-tests
    produces: [none]
    pipelineRef: php-unit-tests
    verification: true
`))
	if err == nil {
		t.Fatal("a verification with no artifact must be refused")
	}
	if !strings.Contains(err.Error(), "at least one artifact") {
		t.Errorf("message = %v", err)
	}
}

// And the converse: a pipeline that is not chained has nothing to publish. A build
// produces an image, not a report, and demanding an artifact would make it
// undeclarable.
func TestAnUnchainedPipelineNeedsNoArtifact(t *testing.T) {
	if _, err := Parse([]byte(`
pipelines:
  - kind: build
    produces: [image]
    pipelineRef: build-image
`)); err != nil {
		t.Fatalf("an ordinary pipeline with no artifact must be accepted: %v", err)
	}
}

/*
 * What a pipeline produces.
 *
 * The declaration replaces a boolean that said two things — "chained after a build" and
 * "re-runnable on its own" — and whose divergence the catalogue in service already carried.
 */

// The product is required, and the message names the accepted values.
//
// Refused at start-up rather than inferred from the name: guessing that a build
// produces an image because it is called `build` would restore the naming
// convention this declaration exists to remove.
func TestTheProductIsRequiredAtDeclaration(t *testing.T) {
	_, err := Parse([]byte(`
pipelines:
  - kind: deploy-recette
    pipelineRef: deploy
    label: Déploiement
`))
	if err == nil {
		t.Fatal("a pipeline with no product must be refused")
	}
	for _, attendu := range []string{"produces", "image", "results", "none"} {
		if !strings.Contains(err.Error(), attendu) {
			t.Errorf("message %q — it must mention %q", err, attendu)
		}
	}
}

// A pipeline that produces nothing declares it, rather than leaving the list
// empty. "This pipeline produces nothing" and "nobody answered" are different, and
// only the second must be refused.
func TestNothingIsAnAnswerThatMustBeDeclared(t *testing.T) {
	parsed, err := Parse([]byte(`
pipelines:
  - kind: deploy-recette
    pipelineRef: deploy
    label: Déploiement
    produces: [none]
`))
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if !parsed.Yields("deploy-recette", ProductNone) {
		t.Error("the declared product is not returned")
	}
	if parsed.Yields("deploy-recette", ProductResults) {
		t.Error("an undeclared product is returned")
	}
}

// A pipeline may produce both: build an image, then run its tests. That is what
// the list is for — splitting it in two to fit the model would be the model
// dictating the pipeline.
func TestAPipelineCanProduceBoth(t *testing.T) {
	parsed, err := Parse([]byte(`
pipelines:
  - kind: build-and-test
    pipelineRef: build-and-test
    label: Construction et tests
    produces: [image, results]
    params:
      reports-upload-url: { source: input }
    uploads:
      reports: { param: reports-upload-url, artifact: reports.tar.gz }
`))
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if !parsed.Yields("build-and-test", ProductImage) ||
		!parsed.Yields("build-and-test", ProductResults) {
		t.Error("both products must be returned")
	}
}

// Deduction covers what is beyond doubt, and nothing more.
//
// `verification: true` already implies publishing reports — validation requires
// it. The rest needs a human answer, which is what surfaced the end-to-end case:
// three artifacts published under a flag that said "no".
func TestDeductionOnlyCoversVerifications(t *testing.T) {
	parsed, err := Parse([]byte(`
pipelines:
  - kind: unit-tests
    pipelineRef: unit
    label: Tests unitaires
    verification: true
    params:
      reports-upload-url: { source: input }
    uploads:
      reports: { param: reports-upload-url, artifact: reports.tar.gz }
`))
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if !parsed.Yields("unit-tests", ProductResults) {
		t.Error("a verification infers its product")
	}

	// A pipeline with no flag cannot be deduced: it is refused.
	if _, err := Parse([]byte(`
pipelines:
  - kind: retag
    pipelineRef: retag-image
    label: Re-tag
`)); err == nil {
		t.Error("a pipeline that is not a verification must declare its product")
	}
}

// An explicit declaration beats deduction: that is what allows fixing a catalogue
// without removing the flag, which still drives chaining.
func TestAnExplicitDeclarationBeatsDeduction(t *testing.T) {
	parsed, err := Parse([]byte(`
pipelines:
  - kind: build-and-verify
    pipelineRef: build-and-verify
    label: Construction vérifiée
    verification: true
    produces: [image, results]
    params:
      reports-upload-url: { source: input }
    uploads:
      reports: { param: reports-upload-url, artifact: reports.tar.gz }
`))
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if !parsed.Yields("build-and-verify", ProductImage) {
		t.Error("deduction overwrote the declaration")
	}
}

// The two inconsistencies the declaration makes detectable, and which used to be
// invisible: results announced that nothing publishes, and artifacts published
// that nothing announces.
func TestInconsistenciesBetweenProductAndArtifactsAreRefused(t *testing.T) {
	// Announced with no artifact: the run runs, concludes, and the screen stays empty.
	_, err := Parse([]byte(`
pipelines:
  - kind: unit-tests
    pipelineRef: unit
    label: Tests unitaires
    produces: [results]
`))
	if err == nil || !strings.Contains(err.Error(), "artifact") {
		t.Errorf("results with no artifact: %v", err)
	}

	// Published without being announced: three artifacts, and nothing saying they are
	// read.
	_, err = Parse([]byte(`
pipelines:
  - kind: e2e-tests
    pipelineRef: e2e
    label: Tests E2E
    produces: [none]
    params:
      reports-upload-url: { source: input }
    uploads:
      reports: { param: reports-upload-url, artifact: reports.tar.gz }
`))
	if err == nil || !strings.Contains(err.Error(), "never read") {
		t.Errorf("artifacts with no announced product: %v", err)
	}
}

// An invented product is refused, and the message names the accepted values.
func TestAnUnknownProductIsRefused(t *testing.T) {
	_, err := Parse([]byte(`
pipelines:
  - kind: bench
    pipelineRef: bench
    label: Charge
    produces: [metrics]
`))
	if err == nil || !strings.Contains(err.Error(), "metrics") {
		t.Fatalf("unknown product: %v", err)
	}
	if !strings.Contains(err.Error(), "results") {
		t.Errorf("message %q — it must name the accepted products", err)
	}
}

// "Nothing" does not accumulate: the declaration would contradict itself, and
// nothing would say which half to believe.
func TestNothingDoesNotCombineWithAnotherProduct(t *testing.T) {
	_, err := Parse([]byte(`
pipelines:
  - kind: build
    pipelineRef: build-image
    label: Construction
    produces: [image, none]
`))
	if err == nil || !strings.Contains(err.Error(), "does not combine") {
		t.Errorf("contradictory products: %v", err)
	}
}

/*
 * What a declaration may fill in, and what the run imposes.
 *
 * The distinction existed, written in Go: a release's plan set `revision`, `version` and
 * `tag-name` **after** the step's parameters, so that a declaration could not change them. It
 * holds for every pipeline, and the catalogue already said it through its `suggest`.
 */

// parseForTest returns a catalogue or fails the test.
func parseForTest(t *testing.T, source string) *Catalog {
	t.Helper()
	parsed, err := Parse([]byte(source))
	if err != nil {
		t.Fatalf("catalogue: %v", err)
	}
	return parsed
}

// Parameters the run imposes are not offered for typing.
//
// Offering them would invite fixing a revision that is then overwritten — worse
// than an absence: one would believe it had been set.
func TestADeclarationIsNotOfferedWhatTheRunImposes(t *testing.T) {
	registry := parseForTest(t, `
pipelines:
  - kind: build
    pipelineRef: build-image
    label: Construction
    produces: [image]
    params:
      revision:   { source: input, required: true, suggest: git-ref }
      version:    { source: input, required: true, suggest: release-tag }
      image-name: { source: input, required: true, suggest: image-name }
      target:     { source: input, required: true, suggest: build-target }
      dockerfile: { source: input }
`)
	pipeline, _ := registry.Find("build")

	offerts := map[string]bool{}
	for _, input := range pipeline.Declared() {
		offerts[input.Name] = true
	}

	for _, impose := range []string{"revision", "version"} {
		if offerts[impose] {
			t.Errorf("%q is offered for typing although the run imposes it", impose)
		}
	}
	for _, declare := range []string{"image-name", "target", "dockerfile"} {
		if !offerts[declare] {
			t.Errorf("%q is not offered — nobody will be able to fill it in", declare)
		}
	}
}

// An image reference is a fallback, not an imposition.
//
// That is what lets a test step aim at the artifact a build step actually produced
// rather than at a recomposed reference that may name an image nobody published.
func TestAnImageReferenceIsAFallbackADeclarationCanOverride(t *testing.T) {
	registry := parseForTest(t, `
pipelines:
  - kind: security-audit
    pipelineRef: audit
    label: Audit
    produces: [results]
    params:
      portal-image: { source: input, required: true, suggest: image, image: portal }
      report-url:   { source: input }
    uploads:
      report-url: { param: report-url, artifact: report.json, family: security }
`)
	pipeline, _ := registry.Find("security-audit")

	input := pipeline.Inputs()[0]
	if input.Origin() != OriginFallback {
		t.Errorf("origine = %q, attendue %q", input.Origin(), OriginFallback)
	}
	// And the fallback stays computable: a step that declares nothing receives what
	// the registry and the tag compose.
	if got := input.FromContext("abc123", "1.8.0", "vcarroy"); got == "" {
		t.Error("the fallback returns nothing — a step with no declaration would have no image")
	}
}

// A parameter with no label falls back to its name.
//
// A catalogue written before labels existed stays fillable: drier, not mute. A
// field without a label is a field nobody fills in.
func TestAParameterWithoutALabelFallsBackToItsName(t *testing.T) {
	registry := parseForTest(t, `
pipelines:
  - kind: build
    pipelineRef: build-image
    label: Construction
    produces: [image]
    params:
      target:     { source: input, required: true, suggest: build-target }
      image-name:
        source: input
        required: true
        suggest: image-name
        label: Published repository
        help: Nom court, sans le registre.
`)
	pipeline, _ := registry.Find("build")

	par := map[string]Input{}
	for _, input := range pipeline.Declared() {
		par[input.Name] = input
	}

	if par["target"].Label != "target" {
		t.Errorf("fallback label = %q, expected the name", par["target"].Label)
	}
	if par["image-name"].Label != "Published repository" || par["image-name"].Help == "" {
		t.Errorf("the declared label is not returned: %+v", par["image-name"])
	}
}
