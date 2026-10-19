// Package catalog carries the declarative description of runnable pipelines.
// See docs/05-pipeline-catalogue.md for the format.
//
// Building a PipelineRun by hand means reproducing what a TriggerTemplate did:
// workspaces, secrets, volume claim templates, service account, image pull
// secrets, defaults. Coding that in Go would let any change on the pipelines'
// side break the dashboard with nothing to signal it.
//
// The catalogue therefore lives in a ConfigMap, outside the binary, and is
// validated at start-up against the Pipelines actually present in the cluster. A
// mismatch stops start-up with a message naming it, rather than producing invalid
// runs.
package catalog

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/images"
)

// DefaultKindConcurrency is the ceiling for a kind that declares none.
//
// Five rather than one: most pipelines — static analysis, unit tests, audit —
// have no reason to exclude each other, and making them queue would lengthen the
// verification of a release without protecting anything.
const DefaultKindConcurrency = 5

// ExclusiveKindConcurrency is the ceiling for a kind that cannot run in
// parallel: integration tests, end-to-end, load. Each deploys to or measures a
// single target, and two runs at once tread on each other.
const ExclusiveKindConcurrency = 1

// Catalog is a loaded catalogue.
type Catalog struct {
	// Capabilities bind the plugins' pipelines to this instance. Resolved into Pipelines at parse.
	Capabilities []Binding `json:"capabilities,omitempty"`
	// Pipelines are the pipelines the dashboard can launch: the bound capabilities, and those still
	// declared whole in the legacy format.
	Pipelines []Pipeline `json:"pipelines,omitempty"`
	// Projects carries what the catalogue declares per project, keyed by slug.
	//
	// In the same file as the pipelines: both describe what the dashboard can
	// trigger, and separating them would mean two mounts and two loads for one
	// intent.
	Projects map[string]ProjectDeclaration `json:"projects,omitempty"`
	// ScoreWeights carries the quality score weighting. Empty applies the default:
	// missing configuration must not make the score incomputable.
	Score map[string]float64 `json:"score,omitempty"`
	// MaxConcurrentRuns protects the cluster: pipelines ask for volumes and build
	// pods, and nothing mechanically prevents launching twenty.
	MaxConcurrentRuns int `json:"maxConcurrentRuns"`
	// PodTemplate applies to every pipeline except those declaring their own.
	//
	// Global rather than repeated per entry, and that fixes an observed failure:
	// every pipeline pulls at least one image from a private registry, and none
	// declared a pull secret. Runs failed with TaskRunImagePullFailed before even
	// starting their first step. Repeating the setting per entry would get it
	// forgotten on the next one.
	PodTemplate *PodTemplate `json:"podTemplate,omitempty"`
}

// Environment describes a deployment environment.
//
// These are configuration, not constants: the label, the rank and above all the
// promotion suffix belong to the deployment — a GitOps controller may know
// `qualif` as `testing`. Hard-coding that mapping means promoting to the wrong
// environment the first time someone forgets.
type Environment struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Rank      int    `json:"rank"`
	Namespace string `json:"namespace"`
	// Deployer carries what the deployment tool needs, in its own vocabulary and
	// without the catalogue knowing it.
	//
	// Named fields used to live here, and the validation required a non-empty
	// promotion suffix on every environment — so a project with no deployment tool
	// could not declare an environment at all. The catalogue imposed one tool on
	// whoever had none.
	//
	// Strings rather than a type: that is what configuration carries, and validation
	// belongs to the deployer, which alone knows what these keys mean.
	Deployer map[string]string `json:"deployer,omitempty"`
	// Observable is false for an environment in a cluster the dashboard cannot see.
	Observable *bool `json:"observable,omitempty"`
	Visible    *bool `json:"visible,omitempty"`
	// Sensitive reserves promotion to the releaser role. It holds for environments
	// that receive what reaches the end user.
	Sensitive *bool `json:"sensitive,omitempty"`
}

// IsObservable returns the effective value; absence means observable.
func (e Environment) IsObservable() bool { return e.Observable == nil || *e.Observable }

// IsVisible returns the effective value; absence means visible.
func (e Environment) IsVisible() bool { return e.Visible == nil || *e.Visible }

// ProjectDeclaration is what the catalogue declares for one project.
type ProjectDeclaration struct {
	// Environments are the environments the project deploys to, in promotion order.
	Environments []Environment `json:"environments,omitempty"`
}

// EnvironmentsOf returns the environments declared for a project, or none.
func (c *Catalog) EnvironmentsOf(project string) []Environment {
	if c == nil {
		return nil
	}
	return c.Projects[project].Environments
}

// IsSensitive returns the effective value; absence means sensitive. An
// environment declared without thinking must be harder to reach, never easier.
func (e Environment) IsSensitive() bool { return e.Sensitive == nil || *e.Sensitive }

/*
 * What a pipeline leaves behind it.
 *
 * The catalogue classified by a boolean, `verification`, whose comment said two things: "a
 * successful build chains this pipeline" **and** "it can be re-run on its own". Two independent
 * properties, a single flag — and the catalogue in service already carried the line where they
 * diverge: `e2e-tests` publishes three artifacts and declares `verification: false`, because the
 * flag expresses the chaining and an E2E campaign targets a shared environment.
 *
 * `Produces` names the second axis, the one `verification` stated only by correlation. It is what
 * makes the contract **checkable**: a pipeline that announces results and publishes none becomes a
 * breach one can point at, instead of an empty screen with nothing saying its cause.
 *
 * The word "family" was not available: it already names a family of test results — `unit`,
 * `static`, `e2e` — and reusing it one level up would have produced two meanings of the same word,
 * permanently.
 */

// Product names something a pipeline produces.
//
// A closed set: these are the outcomes the dashboard can read, and adding one
// takes code — a parser, a screen — not a line of configuration.
type Product string

const (
	// ProductImage: the pipeline publishes images, whose digests the dashboard reads
	// and which a re-tag can promote.
	ProductImage Product = "image"
	// ProductResults: the pipeline publishes reports — families, markers, presigned
	// artifacts. See docs/05-pipeline-catalogue.md.
	ProductResults Product = "results"
	// ProductNone: the pipeline yields only its verdict. A deployment, a migration,
	// a clean-up.
	//
	// Declared explicitly rather than represented by an empty list: "this pipeline
	// produces nothing" and "nobody answered the question" are different, and only
	// the second must be refused.
	ProductNone Product = "none"
)

// products lists the accepted values, to name them in a message.
var products = []Product{ProductImage, ProductResults, ProductNone}

// Pipeline describes a runnable pipeline.
type Pipeline struct {
	// Capability is the capability this pipeline instantiates. Empty for a pipeline declared whole.
	Capability string `json:"-"`
	// Unavailable says why the pipeline cannot run: a need or a parameter the catalogue does not
	// bind. Its runs are refused with this reason.
	Unavailable string `json:"-"`
	// Kind is the identifier the dashboard uses. Distinct from PipelineRef: it
	// survives a rename on the Tekton side, which the catalogue absorbs.
	Kind string `json:"kind"`
	// PipelineRef is the name of the Pipeline in the cluster.
	PipelineRef string `json:"pipelineRef"`
	Label       string `json:"label"`
	// Verification says a successful build chains this pipeline, and that it can be
	// re-run on its own from a release's screen.
	//
	// A process decision — "this project verifies this after building" — which used
	// to live in Go, in two hard-coded lists kept aligned by a test. It belongs in
	// the catalogue, next to the pipeline it describes: a deployment without an
	// end-to-end campaign then stops having a phantom pipeline in its queue.
	//
	// Carried by each pipeline rather than by a list at the top: a list can name an
	// absent family, and the chaining order would become a second truth beside the
	// declaration order.
	Verification bool `json:"verification,omitempty"`
	// Produces lists what the pipeline leaves behind.
	//
	// A list rather than a value: a pipeline that builds an image and then runs its
	// tests yields both, and need not be split in two to fit the model.
	Produces []Product `json:"produces,omitempty"`
	// MaxConcurrent bounds this kind's queue. Zero means DefaultKindConcurrency: a
	// new kind starts parallelisable, which is the common case, and excludes itself
	// by declaring it rather than by forgetting.
	//
	// A ceiling answers the only question asked — how many may run together — and
	// reads without walking the whole catalogue.
	MaxConcurrent int `json:"maxConcurrent,omitempty"`
	// Timeout bounds execution. Empty means Tekton's default.
	Timeout string `json:"timeout,omitempty"`
	// ServiceAccountName is not decorative: a pipeline that deploys into an
	// application namespace needs an account allowed to do so.
	ServiceAccountName string            `json:"serviceAccountName,omitempty"`
	Params             map[string]Param  `json:"params,omitempty"`
	Uploads            map[string]Upload `json:"uploads,omitempty"`
	Workspaces         []Workspace       `json:"workspaces,omitempty"`
	PodTemplate        *PodTemplate      `json:"podTemplate,omitempty"`
}

// Param describes where a parameter's value comes from.
type Param struct {
	// Source is "input" (given at launch), "config" (read from configuration) or
	// "literal" (a fixed value).
	Source   string `json:"source"`
	Key      string `json:"key,omitempty"`
	Value    string `json:"value,omitempty"`
	Required bool   `json:"required,omitempty"`
	// Suggest names the nature of the expected value, so the launch form can offer a
	// list instead of an empty field.
	//
	// Declared here rather than inferred from the parameter's name: `revision`,
	// `app-revision` and `mse-revision` mean the same thing under three names, and
	// coding that correspondence in Go would diverge from the catalogue at the first
	// pipeline added.
	//
	// It has no effect on execution: a suggestion is an aid, not a constraint. It
	// also fills a parameter during automatic chaining, where nobody types anything —
	// it is what says an `app-revision` wants a git ref, without the code knowing
	// that name. See Pipeline.VerificationInputs.
	Suggest string `json:"suggest,omitempty"`
	// Image names the short image repository this parameter must receive. It only
	// means something with `suggest: image`.
	//
	// Declared here because "this parameter wants that image" belongs to the
	// pipeline, not to the dashboard. It used to live in a hard-coded table, copied
	// in two places, which forced a Go change for any pipeline naming its parameters
	// differently.
	Image string `json:"image,omitempty"`
	// Label, Help and Placeholder make the parameter fillable.
	//
	// While a release plan was composed in Go, nobody had to read a parameter name.
	// Declaring the build from the automation screen reverses that: a human chooses a
	// pipeline and must fill in what it asks for, without reading the catalogue.
	//
	// This is what plugin.ActionField already does for actions, and the symmetry is
	// deliberate: both kinds of step are typed into the same form.
	//
	// Optional. Without a label, the parameter's name stands in: an existing
	// catalogue stays usable, only drier.
	Label       string `json:"label,omitempty"`
	Help        string `json:"help,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
}

// Sources reconnues.
const (
	SourceInput   = "input"
	SourceConfig  = "config"
	SourceLiteral = "literal"
)

// Recognised suggestion natures.
//
// Deliberately few: each corresponds to a list the dashboard can build. Adding
// one without the source that populates it would give an empty dropdown, worse
// than a free-text field.
const (
	SuggestGitRef      = "git-ref"
	SuggestReleaseTag  = "release-tag"
	SuggestEnvironment = "environment"
	// SuggestImage expects a full image reference, registry and tag included. Which
	// one is said by Param.Image, so a single nature covers what used to need one per
	// image, and publishing one more image no longer touches the code.
	SuggestImage = "image"
	// SuggestImageName offers the published repositories, for pipelines that take one
	// as a parameter since they handle a single image per run.
	SuggestImageName = "image-name"
	// SuggestBuildTarget offers the Dockerfile targets that feed them.
	SuggestBuildTarget = "build-target"
)

var suggestions = map[string]bool{
	SuggestGitRef: true, SuggestReleaseTag: true, SuggestEnvironment: true,
	SuggestImage: true, SuggestImageName: true, SuggestBuildTarget: true,
}

// Input describes a parameter to supply at launch.
type Input struct {
	Name     string
	Required bool
	Suggest  string
	Image    string
	// Label falls back to the name when the catalogue declares none: a form field
	// without a label is a field nobody fills in.
	Label       string
	Help        string
	Placeholder string
}

// Origin says who fills a parameter in a release's plan.
//
// The distinction existed, written in Go: the build stage set the revision, the
// version and the tag after the step's parameters, so that a declaration could
// not change them. It holds for every pipeline, and the catalogue already said it
// through its suggestions — it only had to be read.
type Origin string

const (
	// OriginContext: the run imposes it and a declaration cannot change it. The
	// revision built and the release tag, and nothing else: two steps of one release
	// aiming at two commits would publish two images under the same tag.
	OriginContext Origin = "context"
	// OriginFallback: the run fills it only in the absence of a declaration.
	//
	// The reference of a published image. With no declaration it is recomposed from
	// the registry and the tag; a declaration may instead point at the artifact a
	// build step actually produced, which is truer and fails when nothing produced
	// it.
	OriginFallback Origin = "fallback"
	// OriginDeclared: only a declaration fills it. The repository to publish, the
	// Dockerfile target, the environment aimed at.
	OriginDeclared Origin = "declared"
)

// Origin classifies this parameter.
func (i Input) Origin() Origin {
	switch i.Suggest {
	case SuggestGitRef, SuggestReleaseTag:
		return OriginContext
	case SuggestImage:
		return OriginFallback
	default:
		return OriginDeclared
	}
}

// Upload declares an expected artifact.
//
// This declaration is what lets the dashboard compute storage keys and mint
// presigned URLs before creating the run: without it, what the pipeline produces
// would have to be guessed.
type Upload struct {
	// Param is the Pipeline parameter that receives the presigned URL. Without it the
	// key would be computed with nobody knowing where to send it.
	Param string `json:"param"`
	// Artifact is the object's name under `raw/<run>/`. It carries the extension of
	// the real content — `.tar.gz` for a packed directory — because the Task uploads
	// what the mode declares, not what it happens to find.
	Artifact string `json:"artifact"`
	Family   string `json:"family"`
	Format   string `json:"format"`
}

// Workspace describes a workspace of the PipelineRun.
type Workspace struct {
	Name                string               `json:"name"`
	Secret              *SecretSource        `json:"secret,omitempty"`
	VolumeClaimTemplate *VolumeClaimTemplate `json:"volumeClaimTemplate,omitempty"`
	EmptyDir            bool                 `json:"emptyDir,omitempty"`
}

// SecretSource names the secret mounted on a workspace.
//
// Two mutually exclusive forms:
//
//	secret: { secretName: git-credentials }         # a literal name
//	secret: { configKey: pipeline.gitSecret }       # a name from configuration
//
// The second exists so a test repository can be targeted without editing the
// versioned catalogue: the credentials that open the real repository are not the
// ones that open a sandbox, and the secret's name is all that distinguishes them.
//
// The name travels, never the material. The dashboard has no read access to
// secrets and must not have any: passing a key as a parameter would land it in
// the PipelineRun's spec, readable by anyone who can get pipelineruns in the
// namespace. Same principle as the presigned URLs — the dashboard designates, it
// does not distribute. See docs/09-security.md.
type SecretSource struct {
	SecretName string `json:"secretName,omitempty"`
	ConfigKey  string `json:"configKey,omitempty"`
}

type VolumeClaimTemplate struct {
	AccessModes      []string `json:"accessModes"`
	StorageClassName string   `json:"storageClassName,omitempty"`
	Storage          string   `json:"storage"`
}

type PodTemplate struct {
	ImagePullSecrets []string `json:"imagePullSecrets,omitempty"`
	// ImagePullSecretsConfigKey names a configuration key holding the secret's name,
	// for the same reason as SecretSource.ConfigKey: targeting another registry must
	// not require editing the versioned catalogue.
	ImagePullSecretsConfigKey string `json:"imagePullSecretsConfigKey,omitempty"`
}

// DefaultMaxConcurrentRuns is the global ceiling applied by default.
//
// Above the per-kind ceiling, and not for comfort: at four, no queue of five
// would ever reach its own and the per-kind setting would be decorative.
const DefaultMaxConcurrentRuns = 10

// Load reads a catalogue from a file.
func Load(path string, options ...Option) (*Catalog, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading catalogue %q: %w", path, err)
	}
	return Parse(content, options...)
}

// Parse parses a catalogue and validates it structurally.
//
// Structural validation is separate from validation against the cluster: the
// first needs nothing, the second needs a connection. Keeping them apart allows
// testing the first without Kubernetes, and gives two error messages that cannot
// be confused.
func Parse(content []byte, options ...Option) (*Catalog, error) {
	var parsing parseOptions
	for _, option := range options {
		option(&parsing)
	}

	var catalog Catalog
	if err := yaml.UnmarshalStrict(content, &catalog); err != nil {
		// UnmarshalStrict refuses unknown fields. A misspelled key in a ConfigMap would
		// otherwise be ignored silently, and the pipeline would run without the workspace
		// or the service account it expects.
		return nil, fmt.Errorf("catalogue illisible: %w", err)
	}

	if catalog.MaxConcurrentRuns == 0 {
		catalog.MaxConcurrentRuns = DefaultMaxConcurrentRuns
	}
	if err := catalog.resolve(parsing.registry); err != nil {
		return nil, fmt.Errorf("catalogue: %w", err)
	}

	// Deduction comes before validation: it fills in what can be inferred safely, and
	// leaves validation to refuse the rest.
	catalog.deduceProducts()

	if err := catalog.validate(); err != nil {
		return nil, err
	}
	return &catalog, nil
}

// deduceProducts completes the pipelines whose product is unambiguous.
//
// One rule only, deliberately: `verification: true` implies publishing reports —
// validation already requires it, since a chained pipeline without artifacts has
// a verdict that never reaches the dashboard.
//
// The rest needs a human answer, because the catalogue carries no signal that
// says it. A build and a re-tag both publish images; neither declares an
// artifact, and nothing structurally distinguishes them from a pipeline that
// deploys. Guessing from their names would restore the naming convention this
// declaration exists to remove.
//
// And the deciding case: an end-to-end pipeline publishes three artifacts with
// `verification: false`. A deduction filing it under "nothing" would perpetuate
// exactly the confusion being fixed.
func (c *Catalog) deduceProducts() {
	for index, pipeline := range c.Pipelines {
		if len(pipeline.Produces) > 0 || !pipeline.Verification {
			continue
		}
		c.Pipelines[index].Produces = []Product{ProductResults}
	}
}

var kindPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func (c *Catalog) validate() error {
	var problems []error

	if c.MaxConcurrentRuns < 1 {
		problems = append(problems, fmt.Errorf("maxConcurrentRuns must be >= 1, got %d", c.MaxConcurrentRuns))
	}
	if len(c.Pipelines) == 0 {
		problems = append(problems, errors.New("the catalogue declares no pipeline"))
	}

	projects := make([]string, 0, len(c.Projects))
	for slug := range c.Projects {
		projects = append(projects, slug)
	}
	sort.Strings(projects)
	for _, slug := range projects {
		if strings.TrimSpace(slug) == "" {
			problems = append(problems, errors.New("a project declaration has no slug"))
		}
		ids := make(map[string]bool, len(c.Projects[slug].Environments))
		ranks := make(map[int]string, len(c.Projects[slug].Environments))
		for _, environment := range c.Projects[slug].Environments {
			if strings.TrimSpace(environment.ID) == "" {
				problems = append(problems, fmt.Errorf("project %q: an environment is declared without an id", slug))
			}
			if ids[environment.ID] {
				problems = append(problems, fmt.Errorf("project %q: environment %q declared twice", slug, environment.ID))
			}
			ids[environment.ID] = true
			// What the catalogue does not check: that the promotion suffix exists and is
			// unique. That is the deployer's rule, checked by it against the cluster's real
			// policies.
			if other, clash := ranks[environment.Rank]; clash {
				problems = append(problems, fmt.Errorf(
					"project %q: environments %q and %q share rank %d", slug, other, environment.ID, environment.Rank))
			}
			ranks[environment.Rank] = environment.ID
		}
	}

	seen := make(map[string]bool, len(c.Pipelines))
	for _, pipeline := range c.Pipelines {
		if seen[pipeline.Kind] {
			// A duplicate would make launching non-deterministic: two entries for one kind,
			// and nothing says which applies.
			problems = append(problems, fmt.Errorf("kind %q declared twice", pipeline.Kind))
		}
		seen[pipeline.Kind] = true
		problems = append(problems, pipeline.validate()...)
	}

	return errors.Join(problems...)
}

func (p Pipeline) validate() []error {
	var problems []error

	if !kindPattern.MatchString(p.Kind) {
		problems = append(problems, fmt.Errorf(
			"kind %q invalide: minuscules, chiffres et tirets uniquement", p.Kind))
	}
	if strings.TrimSpace(p.PipelineRef) == "" {
		problems = append(problems, fmt.Errorf("kind %q: pipelineRef is required", p.Kind))
	}
	// A verification with no declared artifact has a verdict that never reaches the
	// dashboard: the run runs, concludes, and the release keeps waiting for a result
	// nothing reports. Refused at start-up rather than discovered after a
	// forty-minute campaign.
	if p.Verification && len(p.Uploads) == 0 {
		problems = append(problems, fmt.Errorf(
			"kind %q: a verification must declare at least one artifact", p.Kind))
	}
	problems = append(problems, p.validateProducts()...)
	if p.MaxConcurrent < 0 {
		problems = append(problems, fmt.Errorf(
			"kind %q: maxConcurrent must be >= 1 (or absent), got %d", p.Kind, p.MaxConcurrent))
	}

	for name, param := range p.Params {
		switch param.Source {
		case SourceInput:
			if param.Suggest != "" && !suggestions[param.Suggest] {
				problems = append(problems, fmt.Errorf(
					"kind %q, parameter %q: unknown suggestion %q", p.Kind, name, param.Suggest))
			}
			// An unnamed image cannot be guessed: chaining would yield a truncated
			// reference, and the run would fail on a pull with a message that does not point
			// at the catalogue.
			if param.Suggest == SuggestImage && param.Image == "" {
				problems = append(problems, fmt.Errorf(
					"kind %q, parameter %q: suggestion %q requires the image name",
					p.Kind, name, SuggestImage))
			}
			if param.Image != "" && param.Suggest != SuggestImage {
				problems = append(problems, fmt.Errorf(
					"kind %q, parameter %q: image named without suggestion %q",
					p.Kind, name, SuggestImage))
			}
		case SourceConfig:
			if param.Suggest != "" {
				// A suggestion on a parameter the user never types would be displayed nowhere:
				// reporting it avoids believing the form is broken.
				problems = append(problems, fmt.Errorf(
					"kind %q, parameter %q: suggestion on a %q source", p.Kind, name, param.Source))
			}
			if param.Key == "" {
				problems = append(problems, fmt.Errorf(
					"kind %q, parameter %q: a config source requires a key", p.Kind, name))
			}
		case SourceLiteral:
			if param.Value == "" {
				problems = append(problems, fmt.Errorf(
					"kind %q, parameter %q: a literal source requires a value", p.Kind, name))
			}
		default:
			problems = append(problems, fmt.Errorf(
				"kind %q, parameter %q: unknown source %q (expected %s, %s or %s)",
				p.Kind, name, param.Source, SourceInput, SourceConfig, SourceLiteral))
		}
	}

	declared := make(map[string]bool, len(p.Workspaces))
	for _, workspace := range p.Workspaces {
		if declared[workspace.Name] {
			problems = append(problems, fmt.Errorf(
				"kind %q: workspace %q declared twice", p.Kind, workspace.Name))
		}
		declared[workspace.Name] = true

		name := workspace.Name
		sources := 0
		if workspace.Secret != nil {
			sources++
		}
		if workspace.VolumeClaimTemplate != nil {
			sources++
		}
		if workspace.EmptyDir {
			sources++
		}
		if sources != 1 {
			problems = append(problems, fmt.Errorf(
				"kind %q, workspace %q: exactly one source expected (secret, volumeClaimTemplate or emptyDir)",
				p.Kind, name))
		}
	}

	return problems
}

// validateProducts refuses a product that is missing, unknown, or contradicted by
// what the pipeline declares elsewhere.
//
// Absence is refused at start-up: incomplete configuration discovered at the
// first click surfaces on the rarest operation, while a refusal at start-up is a
// deployment error, visible immediately and to the right person.
//
// The cost of letting it pass is known: with no declared product, the dashboard
// cannot tell a silent run that had nothing to say from one in breach of the
// contract — precisely the diagnosis this makes possible.
func (p Pipeline) validateProducts() []error {
	if len(p.Produces) == 0 {
		return []error{fmt.Errorf(
			"kind %q: `produces` is required (%s) — what a pipeline leaves "+
				"behind cannot be guessed, and inferring it from its name "+
				"would restore the convention this declaration replaces",
			p.Kind, joinProducts(products))}
	}

	var problems []error
	seen := map[Product]bool{}

	for _, product := range p.Produces {
		switch product {
		case ProductImage, ProductResults, ProductNone:
		default:
			problems = append(problems, fmt.Errorf(
				"kind %q: unknown product %q (accepted: %s)",
				p.Kind, product, joinProducts(products)))
			continue
		}
		if seen[product] {
			problems = append(problems, fmt.Errorf(
				"kind %q: product %q declared twice", p.Kind, product))
		}
		seen[product] = true
	}

	// "Nothing" does not accumulate: a pipeline declaring `none` beside another
	// product contradicts itself, and nothing says which half to believe.
	if seen[ProductNone] && len(seen) > 1 {
		problems = append(problems, fmt.Errorf(
			"kind %q: %q does not combine with another product", p.Kind, ProductNone))
	}

	// Results announced without a declared artifact would never arrive: the run runs,
	// concludes, and the screen stays empty. The same check as for a verification,
	// now hung off the product rather than the flag.
	if seen[ProductResults] && len(p.Uploads) == 0 {
		problems = append(problems, fmt.Errorf(
			"kind %q: announces results without declaring an artifact — its verdict "+
				"would never reach the dashboard", p.Kind))
	}

	// The opposite mismatch is refused too: published artifacts that nothing reads
	// back.
	if !seen[ProductResults] && len(p.Uploads) > 0 {
		problems = append(problems, fmt.Errorf(
			"kind %q: declares %d artifact(s) without announcing %q — they would be "+
				"published and never read", p.Kind, len(p.Uploads), ProductResults))
	}

	return problems
}

func joinProducts(list []Product) string {
	names := make([]string, 0, len(list))
	for _, product := range list {
		names = append(names, string(product))
	}
	return strings.Join(names, ", ")
}

// Yields says whether the pipeline produces this thing.
func (p Pipeline) Yields(product Product) bool {
	return slices.Contains(p.Produces, product)
}

// Yields says whether the named kind produces this thing.
//
// Answered by the catalogue rather than computed by the caller: ingestion, facts
// and plan validation all ask it, and three ways of asking would eventually
// diverge.
func (c *Catalog) Yields(kind string, product Product) bool {
	pipeline, found := c.Find(kind)
	return found && pipeline.Yields(product)
}

// Concurrency returns a kind's effective ceiling.
func (p Pipeline) Concurrency() int {
	if p.MaxConcurrent > 0 {
		return p.MaxConcurrent
	}
	return DefaultKindConcurrency
}

// Concurrency returns the declared ceilings, by kind.
//
// Kinds absent from the catalogue — local tasks, which never become PipelineRuns
// — are not listed: the scheduler applies the default to them, as to a new kind.
func (c *Catalog) Concurrency() map[string]int {
	limits := make(map[string]int, len(c.Pipelines))
	for _, pipeline := range c.Pipelines {
		limits[pipeline.Kind] = pipeline.Concurrency()
	}
	return limits
}

// Find returns a kind's entry.
func (c *Catalog) Find(kind string) (Pipeline, bool) {
	for _, pipeline := range c.Pipelines {
		if pipeline.Kind == kind {
			// The global pod template applies to whoever declares none. Inherited here rather
			// than at load time: the catalogue then stays an exact reflection of the file,
			// and what is inherited reads in a single function.
			if pipeline.PodTemplate == nil {
				pipeline.PodTemplate = c.PodTemplate
			}
			return pipeline, true
		}
	}
	return Pipeline{}, false
}

// Kinds returns the declared kinds, sorted.
func (c *Catalog) Kinds() []string {
	kinds := make([]string, 0, len(c.Pipelines))
	for _, pipeline := range c.Pipelines {
		kinds = append(kinds, pipeline.Kind)
	}
	sort.Strings(kinds)
	return kinds
}

// RequiredInputs returns the parameters a launch must provide.
func (p Pipeline) RequiredInputs() []string {
	var required []string
	for name, param := range p.Params {
		if param.Source == SourceInput && param.Required {
			required = append(required, name)
		}
	}
	sort.Strings(required)
	return required
}

// VerificationInputs fills the mandatory inputs of a chained run.
//
// The values come from what the catalogue declares it wants, not from a table of
// names kept in the code. That table existed twice — one copy in automatic
// chaining, one in manual re-runs — and listed five names specific to one
// project, which a pipeline naming its parameters differently could not use.
//
// It returns the name of the parameter left empty rather than an incomplete
// table: a run created without its required inputs fails at scheduling, with a
// message that does not name the cause.
func (p Pipeline) VerificationInputs(revision, tag, registry string) (map[string]string, string) {
	params := map[string]string{}

	for _, input := range p.Inputs() {
		if !input.Required {
			continue
		}

		value := input.FromContext(revision, tag, registry)
		if value == "" {
			return nil, input.Name
		}
		params[input.Name] = value
	}
	return params, ""
}

// FromContext returns the value a release's run gives this parameter.
//
// Empty when it gives none: it is then for the declaration to fill it, and
// [Input.Origin] says which of the two has the last word.
func (i Input) FromContext(revision, tag, registry string) string {
	switch i.Suggest {
	case SuggestGitRef:
		return revision
	case SuggestReleaseTag:
		return tag
	case SuggestImage:
		// Verification analyses the images the build published, never a rebuild: a
		// rebuild would produce a different artifact from the one that will be deployed,
		// and the verdict would miss its target.
		return images.Reference(registry, i.Image, tag)
	default:
		return ""
	}
}

// Declared returns the inputs a declaration may fill.
//
// Everything except those the run imposes: offering them would invite typing a
// revision that is then overwritten, which is worse than an absence — one would
// believe it had been set.
func (p Pipeline) Declared() []Input {
	var declared []Input
	for _, input := range p.Inputs() {
		if input.Origin() == OriginContext {
			continue
		}
		declared = append(declared, input)
	}
	return declared
}

// Inputs returns every parameter typed at launch, required or not.
//
// Distinct from RequiredInputs, which serves to refuse an incomplete launch: the
// form must also show the optional ones — a parameter nobody sees is a parameter
// nobody ever fills in.
func (p Pipeline) Inputs() []Input {
	inputs := make([]Input, 0, len(p.Params))
	for name, param := range p.Params {
		if param.Source != SourceInput {
			continue
		}
		label := param.Label
		if strings.TrimSpace(label) == "" {
			label = name
		}
		inputs = append(inputs, Input{
			Name: name, Required: param.Required,
			Suggest: param.Suggest, Image: param.Image,
			Label: label, Help: param.Help, Placeholder: param.Placeholder,
		})
	}
	// Required first, then alphabetical: a Go map has no order, and a form whose
	// fields move on every opening is unusable.
	sort.Slice(inputs, func(i, j int) bool {
		if inputs[i].Required != inputs[j].Required {
			return inputs[i].Required
		}
		return inputs[i].Name < inputs[j].Name
	})
	return inputs
}

// Family returns the signal family this pipeline measures.
//
// Empty when it declares none, or several, in which case nothing says which one
// to attach a tool to: better not to classify than to classify at random, since a
// wrong family moves a verdict onto a signal that was never measured.
//
// Read from the uploads, which already declare it to file artifacts. The marker
// channel uses it to attribute a family to a tool that published nothing — it
// then has no artifact to infer it from, which is precisely the case worth making
// visible.
func (p Pipeline) Family() string {
	var found string
	for _, upload := range p.Uploads {
		if upload.Family == "" || upload.Family == found {
			continue
		}
		if found != "" {
			return ""
		}
		found = upload.Family
	}
	return found
}

// VerificationKinds lists what a successful build chains.
//
// In declaration order, which becomes queueing order: a Go map has no order, and
// runs that change rank on every build make two releases impossible to compare at
// a glance.
//
// It also decides what can be re-run individually. Both questions have the same
// answer, and keeping them separate gave a button that could launch what the
// chain did not — or a chained family that could not be caught up without
// rebuilding the whole release.
func (c *Catalog) VerificationKinds() []string {
	kinds := make([]string, 0, len(c.Pipelines))
	for _, pipeline := range c.Pipelines {
		if pipeline.Verification {
			kinds = append(kinds, pipeline.Kind)
		}
	}
	return kinds
}

// IsVerification says whether a family is a chained verification.
func (c *Catalog) IsVerification(kind string) bool {
	pipeline, found := c.Find(kind)
	return found && pipeline.Verification
}

// SuggestionKey names the list to offer for this input.
//
// The nature alone no longer suffices now that `image` covers them all: two
// parameters both wanting an image reference do not expect the same list. The key
// therefore combines nature and image, and it — not the parameter's name — indexes
// the lists sent to the form.
func (i Input) SuggestionKey() string {
	if i.Suggest == "" || i.Image == "" {
		return i.Suggest
	}
	return i.Suggest + ":" + i.Image
}

// SuggestedImages returns the image repositories the catalogue declares it
// expects.
//
// Building the lists from there rather than from an enumeration in Go means a
// pipeline referencing one more image gets its dropdown without touching the
// dashboard.
func (c *Catalog) SuggestedImages() []string {
	seen := map[string]bool{}
	for _, pipeline := range c.Pipelines {
		for _, param := range pipeline.Params {
			if param.Suggest == SuggestImage && param.Image != "" {
				seen[param.Image] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ScoreWeights returns the score weighting in the shape the computation expects.
//
// Converted here rather than at load time: the catalogue is generic YAML, and
// teaching it another package's type would tie it to an implementation it need
// not know.
func (c *Catalog) ScoreWeights() map[string]float64 {
	if len(c.Score) == 0 {
		return nil
	}
	weights := make(map[string]float64, len(c.Score))
	for signal, weight := range c.Score {
		weights[signal] = weight
	}
	return weights
}

// ResolveSecrets returns a copy of the pipeline with secret names resolved from
// configuration.
//
// Resolved before creating the PipelineRun rather than when loading the
// catalogue, which happens once at start-up: a missing configuration key must
// report itself when it is missing, naming the kind that asks for it.
func (p Pipeline) ResolveSecrets(config map[string]string) (Pipeline, error) {
	resolved := p
	resolved.Workspaces = make([]Workspace, len(p.Workspaces))
	copy(resolved.Workspaces, p.Workspaces)

	if template := resolved.PodTemplate; template != nil && template.ImagePullSecretsConfigKey != "" {
		name, present := config[template.ImagePullSecretsConfigKey]
		if !present || name == "" {
			return Pipeline{}, fmt.Errorf(
				"kind %q, image pull secret: configuration key %q is missing",
				p.Kind, template.ImagePullSecretsConfigKey)
		}
		// Copied, for the same reason as the workspaces: the catalogue is shared.
		resolved.PodTemplate = &PodTemplate{ImagePullSecrets: []string{name}}
	}

	for i, workspace := range resolved.Workspaces {
		if workspace.Secret == nil || workspace.Secret.ConfigKey == "" {
			continue
		}

		name, present := config[workspace.Secret.ConfigKey]
		if !present || name == "" {
			return Pipeline{}, fmt.Errorf(
				"kind %q, workspace %q: configuration key %q is missing",
				p.Kind, workspace.Name, workspace.Secret.ConfigKey)
		}

		// Copying the pointer is necessary: without it, resolution would write into the
		// shared catalogue, and the second run would see the first run's resolved name as
		// if it were literal.
		resolved.Workspaces[i].Secret = &SecretSource{SecretName: name}
	}

	return resolved, nil
}

// ServiceAccounts returns the service accounts the pipelines use.
//
// Sorted and deduplicated. A project's key must be mountable by every one of
// them, failing which it would work for some of its pipelines and not others —
// and the failure would point at the clone rather than at the mount.
//
// The default account, used by a pipeline naming none, is returned as `default`:
// that is what Kubernetes calls it, and it is what must be granted.
func (c *Catalog) ServiceAccounts() []string {
	seen := map[string]bool{}
	for _, pipeline := range c.Pipelines {
		name := strings.TrimSpace(pipeline.ServiceAccountName)
		if name == "" {
			name = "default"
		}
		seen[name] = true
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
