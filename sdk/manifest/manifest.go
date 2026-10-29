// Package manifest is the schema of a plugin an instance installs without recompiling.
//
// Such a plugin is a directory: `arkhitekton-plugin.yaml` at its root, the Tekton definitions of its
// pipelines, and how their runs are read. It may bring a program the dashboard launches and talks to
// over gRPC (the rpc module): its routes, its tables, its module, its extension hooks, its
// interface. The dashboard converts the YAML to JSON and reads it with this package.
//
// The administrator who installs a plugin trusts it: its program runs as the dashboard does. What
// stays closed is what it may ask of the cluster: its Tekton definitions choose no service account,
// and its pipelines need nothing a binding from the screen cannot give.
package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// APIVersion is the schema version a manifest states.
const APIVersion = "arkhitekton.plugin/v2"

// Protocol is the version of the contract a plugin's program speaks (rpc.ProtocolVersion).
const Protocol = 1

// FileName is the manifest's name at the root of the plugin's directory.
const FileName = "arkhitekton-plugin.yaml"

// MaxSize bounds a plugin's directory, every file counted, its program included.
const MaxSize = 256 << 20

// A plugin is distributed as an OCI artifact: an empty config of ConfigMediaType, and one layer of
// LayerMediaType, the directory as a gzipped tar. A plugin with a program is an index of such
// artifacts, one per platform, each carrying the program built for it.
const (
	ConfigMediaType = "application/vnd.arkhitekton.plugin.config.v1+json"
	LayerMediaType  = "application/vnd.arkhitekton.plugin.layer.v1.tar+gzip"
)

// Manifest is what an installable plugin says about itself.
type Manifest struct {
	APIVersion string `json:"apiVersion"`
	ID         string `json:"id"`
	// Version changes whenever interpretation changes, as for a compiled plugin.
	Version string `json:"version"`
	// SDK is the sdk.APIVersion the plugin was built against.
	SDK      string   `json:"sdk"`
	Label    string   `json:"label"`
	Summary  string   `json:"summary,omitempty"`
	Settings Settings `json:"settings,omitzero"`
	// PerProject says a project decides whether the plugin is switched on for it.
	PerProject bool       `json:"perProject,omitempty"`
	Pipelines  []Pipeline `json:"pipelines,omitempty"`

	// Binary is the plugin's program, a path in its directory; Protocol, the contract it speaks.
	Binary   string `json:"binary,omitempty"`
	Protocol int    `json:"protocol,omitempty"`
	// Migrations are the plugin's tables, applied when it is installed.
	Migrations *Migrations `json:"migrations,omitempty"`
	// Events are the facts the plugin states, by suffix: the dashboard prefixes them with its id.
	Events []capability.EventSpec `json:"events,omitempty"`
	// Module says the program runs a module, under the worker's lease, while a project switches the
	// plugin on.
	Module bool `json:"module,omitempty"`
	// Hooks are the extension points the program answers.
	Hooks []Hook `json:"hooks,omitempty"`
	// Routes are the API routes the program serves, under the plugin's prefix.
	Routes []Route `json:"routes,omitempty"`
	// UI is the plugin's interface module, loaded by the dashboard's interface.
	UI *UI `json:"ui,omitempty"`

	// Tracker and Forge say the program connects to a tracker or a forge, as a compiled plugin's
	// implementation does. One of each is switched on per instance: such a plugin is installed
	// switched off.
	Tracker bool `json:"tracker,omitempty"`
	Forge   bool `json:"forge,omitempty"`
	// Probes are the probes the program runs, their schedule decided here.
	Probes []Probe `json:"probes,omitempty"`
	// Actions are the steps the program carries out.
	Actions []capability.ActionSpec `json:"actions,omitempty"`
}

// Probe is a probe the plugin's program runs.
type Probe struct {
	// Name names the probe's cursor within the plugin: lower case, digits, dashes and underscores.
	Name string `json:"name"`
	// Interval is a duration, `5m`; empty, the deployment's floor. The floor wins over a shorter one.
	Interval string `json:"interval,omitempty"`
	// Eager asks for a pass at start-up, for a source that fills a screen.
	Eager bool `json:"eager,omitempty"`
}

// Schedule returns the probe's schedule. An interval that does not parse is refused by Validate.
func (p Probe) Schedule() capability.Schedule {
	interval, _ := time.ParseDuration(p.Interval)
	return capability.Schedule{Interval: interval, Eager: p.Eager}
}

// Migrations are SQL files applied in order, with their own version table.
type Migrations struct {
	Dir   string `json:"dir"`
	Table string `json:"table"`
}

// Hook is an extension point a plugin's program answers.
type Hook string

const (
	HookDeployments            Hook = "deployments"
	HookPromotions             Hook = "promotions"
	HookEnvironmentSuggestions Hook = "environmentSuggestions"
	HookSchemeCheck            Hook = "schemeCheck"
	HookPromotionFacts         Hook = "promotionFacts"
	HookProjectSettings        Hook = "projectSettings"
)

// Hooks are every extension point a plugin may answer.
var Hooks = []Hook{
	HookDeployments, HookPromotions, HookEnvironmentSuggestions, HookSchemeCheck, HookPromotionFacts,
	HookProjectSettings,
}

// Route is an API route the plugin's program serves. The dashboard authenticates the caller,
// checks the permission and, for a project's route, that the project switches the plugin on,
// before the program sees the request.
type Route struct {
	Method string `json:"method"`
	// Path is below the plugin's prefix: segments, `{name}` for a parameter, and a final
	// `{name...}` for the rest of the path.
	Path string `json:"path"`
	// Permission is the caller's: `read`, `release:edit`… or `deploy:{param}`, an environment
	// taken from the path.
	Permission string `json:"permission"`
	// Scope is `project` (the default), under `/projects/{project}/plugins/<id>/`, or `instance`,
	// under `/plugins/<id>/`.
	Scope string `json:"scope,omitempty"`
}

// Route scopes.
const (
	ScopeProject  = "project"
	ScopeInstance = "instance"
)

// UI is the plugin's interface: its screens, menu entries and blocks as resources (sdk/ui), and
// the script that registers its components with the stylesheets it needs.
type UI struct {
	Entry  string   `json:"entry,omitempty"`
	Styles []string `json:"styles,omitempty"`
	// Resources are YAML files, or patterns matching them, of interface resources.
	Resources []string `json:"resources,omitempty"`
}

// InterpreterNative says the plugin's program reads the pipeline's runs.
const InterpreterNative = "native"

// Settings are the instance and project settings the plugin expects.
type Settings struct {
	Global  []capability.Field `json:"global,omitempty"`
	Project []capability.Field `json:"project,omitempty"`
}

// Pipeline is a pipeline capability. Paths are relative to the plugin's directory.
type Pipeline struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	PipelineRef string `json:"pipelineRef"`
	// Tekton lists the files of the Pipeline and of the Tasks it brings, one definition per file.
	// Empty, the operator provides the Pipeline.
	Tekton []string `json:"tekton,omitempty"`
	// ExternalTasks are Tasks the pipeline uses without bringing them.
	ExternalTasks []string             `json:"externalTasks,omitempty"`
	Params        map[string]Param     `json:"params,omitempty"`
	Workspaces    []Workspace          `json:"workspaces,omitempty"`
	Needs         []Need               `json:"needs,omitempty"`
	Artifacts     []Artifact           `json:"artifacts,omitempty"`
	Produces      []capability.Product `json:"produces"`
	Verification  bool                 `json:"verification,omitempty"`
	Exclusive     bool                 `json:"exclusive,omitempty"`
	Group         string               `json:"group,omitempty"`
	// Reading or Interpreter, exactly one when the pipeline publishes results: a declared reading,
	// or `native`, the plugin's program.
	Reading     string `json:"reading,omitempty"`
	Interpreter string `json:"interpreter,omitempty"`
}

// Param describes a Pipeline parameter.
type Param struct {
	Source      capability.ParamSource `json:"source"`
	DefaultKey  string                 `json:"defaultKey,omitempty"`
	Value       string                 `json:"value,omitempty"`
	Required    bool                   `json:"required,omitempty"`
	Label       string                 `json:"label,omitempty"`
	Help        string                 `json:"help,omitempty"`
	Placeholder string                 `json:"placeholder,omitempty"`
	// Suggest names what the launch form offers for an input: `git-ref`, `release-tag`, `image`,
	// `environment`…; Image, the short repository a `suggest: image` expects.
	Suggest string `json:"suggest,omitempty"`
	Image   string `json:"image,omitempty"`
}

// Workspace is a Pipeline workspace, backed by the secret bound to a need, a claim per run or an
// empty directory.
type Workspace struct {
	Name     string              `json:"name"`
	Need     capability.NeedName `json:"need,omitempty"`
	Volume   *Volume             `json:"volume,omitempty"`
	EmptyDir bool                `json:"emptyDir,omitempty"`
}

// Volume is a claim created for each run.
type Volume struct {
	Storage string `json:"storage"`
}

// Need is a privilege the pipeline declares, with why it needs it.
type Need struct {
	Name   capability.NeedName `json:"name"`
	Reason string              `json:"reason"`
}

// Artifact is something the pipeline uploads to the presigned URL it is given.
type Artifact struct {
	Name    string                    `json:"name"`
	Param   string                    `json:"param"`
	Object  string                    `json:"object"`
	Format  capability.ArtifactFormat `json:"format"`
	Members []Member                  `json:"members"`
}

// Member says how to read a file of an artifact.
type Member struct {
	Match     string   `json:"match"`
	Parser    parse.ID `json:"parser"`
	Tool      string   `json:"tool"`
	MediaType string   `json:"mediaType,omitempty"`
}

var (
	// versionPattern keeps a version usable as a path segment, where it is stored.
	versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.-]*$`)
	// tablePattern is a PostgreSQL identifier, quoted nowhere.
	tablePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
	// segmentPattern is a literal segment of a route's path.
	segmentPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	// parameterPattern is a route's parameter, `{name}` or a final `{name...}`.
	parameterPattern = regexp.MustCompile(`^\{([a-z][a-zA-Z0-9_]*)(\.\.\.)?\}$`)
	// permissionPattern is a permission's name, or `deploy:{param}`.
	permissionPattern = regexp.MustCompile(`^(?:[a-z]+(?::[a-z]+)?|deploy:\{([a-z][a-zA-Z0-9_]*)\})$`)
	// suffixPattern is an event's or an action's suffix.
	suffixPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
	// fieldPattern is an action's parameter, named in the payload's templates.
	fieldPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// Decode reads a manifest from JSON, refusing unknown keys: a misspelt key is a declaration that
// would silently not apply, and a key the schema does not have, a service account for one, is a
// privilege the plugin is not given.
func Decode(raw []byte) (Manifest, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("plugin manifest: %w", err)
	}
	return manifest, manifest.Validate()
}

// Validate checks what can be checked without the plugin's files.
func (m Manifest) Validate() error {
	var problems []error
	switch m.APIVersion {
	case APIVersion:
	case "arkhitekton.plugin/v1":
		problems = append(problems, fmt.Errorf("apiVersion %q: this dashboard installs %q, rebuild the plugin", m.APIVersion, APIVersion))
	default:
		problems = append(problems, fmt.Errorf("apiVersion %q, expected %q", m.APIVersion, APIVersion))
	}
	if !capability.ValidName(m.ID) {
		problems = append(problems, fmt.Errorf("id %q: lower case, digits and dashes only", m.ID))
	}
	if !versionPattern.MatchString(m.Version) || strings.TrimSpace(m.Label) == "" {
		problems = append(problems, fmt.Errorf("version %q: letters, digits, dots and dashes; and a label", m.Version))
	}
	if major, _, _ := strings.Cut(m.SDK, "."); major != sdk.APIVersion {
		problems = append(problems, fmt.Errorf("built against sdk %q, this schema is sdk %q", m.SDK, sdk.APIVersion))
	}
	if len(m.Pipelines) == 0 && m.Binary == "" {
		problems = append(problems, errors.New("neither pipeline nor program: the plugin brings nothing"))
	}
	problems = append(problems, m.programProblems()...)
	names := map[string]bool{}
	refs := map[string]bool{}
	for i, pipeline := range m.Pipelines {
		at := fmt.Sprintf("pipelines[%d]", i)
		if names[pipeline.Name] {
			problems = append(problems, fmt.Errorf("%s: %q declared twice", at, pipeline.Name))
		}
		names[pipeline.Name] = true
		if refs[pipeline.PipelineRef] {
			problems = append(problems, fmt.Errorf("%s: Tekton Pipeline %q claimed twice", at, pipeline.PipelineRef))
		}
		refs[pipeline.PipelineRef] = true
		problems = append(problems, pipeline.problems(at)...)
		if pipeline.Interpreter == InterpreterNative && m.Binary == "" {
			problems = append(problems, fmt.Errorf("%s: read by the plugin's program, and the plugin has none", at))
		}
	}
	return errors.Join(problems...)
}

// programProblems checks what the plugin's program brings, and that it has one if it brings
// anything.
func (m Manifest) programProblems() []error {
	var problems []error
	if m.Binary == "" {
		for _, brought := range []struct {
			present bool
			what    string
		}{
			{m.Module, "a module"}, {len(m.Hooks) > 0, "hooks"}, {len(m.Routes) > 0, "routes"}, {m.UI != nil, "an interface"},
			{m.Tracker, "a tracker"}, {m.Forge, "a forge"}, {len(m.Probes) > 0, "probes"}, {len(m.Actions) > 0, "actions"},
		} {
			if brought.present {
				problems = append(problems, fmt.Errorf("%s need a program: binary is empty", brought.what))
			}
		}
	} else {
		if !fs.ValidPath(m.Binary) || m.Binary == "." {
			problems = append(problems, fmt.Errorf("binary %q is not a path inside the plugin", m.Binary))
		}
		if m.Protocol != Protocol {
			problems = append(problems, fmt.Errorf("protocol %d, this dashboard speaks %d", m.Protocol, Protocol))
		}
	}
	if migrations := m.Migrations; migrations != nil {
		if !fs.ValidPath(migrations.Dir) || migrations.Dir == "." {
			problems = append(problems, fmt.Errorf("migrations.dir %q is not a path inside the plugin", migrations.Dir))
		}
		if !tablePattern.MatchString(migrations.Table) || migrations.Table == "goose_db_version" {
			problems = append(problems, fmt.Errorf("migrations.table %q: an identifier of its own", migrations.Table))
		}
	}
	suffixes := map[string]bool{}
	for i, event := range m.Events {
		if !suffixPattern.MatchString(event.Suffix) || strings.TrimSpace(event.Label) == "" || suffixes[event.Suffix] {
			problems = append(problems, fmt.Errorf("events[%d]: a unique suffix (%q) and a label", i, event.Suffix))
		}
		suffixes[event.Suffix] = true
	}
	problems = append(problems, m.providerProblems()...)
	hooks := map[Hook]bool{}
	for _, hook := range m.Hooks {
		if !slices.Contains(Hooks, hook) || hooks[hook] {
			problems = append(problems, fmt.Errorf("hooks: %q unknown or declared twice", hook))
		}
		hooks[hook] = true
	}
	routes := map[string]bool{}
	for i, route := range m.Routes {
		at := fmt.Sprintf("routes[%d]", i)
		problems = append(problems, route.problems(at)...)
		key := route.Method + " " + route.EffectiveScope() + " " + route.Path
		if routes[key] {
			problems = append(problems, fmt.Errorf("%s: %s %s declared twice", at, route.Method, route.Path))
		}
		routes[key] = true
	}
	if ui := m.UI; ui != nil {
		if ui.Entry == "" && len(ui.Resources) == 0 {
			problems = append(problems, errors.New("ui: an entry, resources, or both"))
		}
		if ui.Entry == "" && len(ui.Styles) > 0 {
			problems = append(problems, errors.New("ui.styles: an entry's"))
		}
		if ui.Entry != "" && (!fs.ValidPath(ui.Entry) || !strings.HasSuffix(ui.Entry, ".js")) {
			problems = append(problems, fmt.Errorf("ui.entry %q: a .js file inside the plugin", ui.Entry))
		}
		for _, resource := range ui.Resources {
			if !fs.ValidPath(resource) || !(strings.HasSuffix(resource, ".yaml") || strings.HasSuffix(resource, ".yml")) {
				problems = append(problems, fmt.Errorf("ui.resources: %q, YAML files inside the plugin", resource))
			}
		}
		for _, style := range ui.Styles {
			if !fs.ValidPath(style) || !strings.HasSuffix(style, ".css") {
				problems = append(problems, fmt.Errorf("ui.styles: %q, a .css file inside the plugin", style))
			}
		}
	}
	return problems
}

// EffectiveScope is the route's scope, a project's when it says none.
func (r Route) EffectiveScope() string {
	if r.Scope == "" {
		return ScopeProject
	}
	return r.Scope
}

// DeployParameter returns the path parameter a `deploy:{param}` permission names.
func (r Route) DeployParameter() (string, bool) {
	match := permissionPattern.FindStringSubmatch(r.Permission)
	if match == nil || match[1] == "" {
		return "", false
	}
	return match[1], true
}

func (r Route) problems(at string) []error {
	var problems []error
	switch r.Method {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
	default:
		problems = append(problems, fmt.Errorf("%s: method %q", at, r.Method))
	}
	if r.EffectiveScope() != ScopeProject && r.EffectiveScope() != ScopeInstance {
		problems = append(problems, fmt.Errorf("%s: scope %q, expected project or instance", at, r.Scope))
	}
	parameters := map[string]bool{}
	segments := strings.Split(r.Path, "/")
	for i, segment := range segments {
		if match := parameterPattern.FindStringSubmatch(segment); match != nil {
			if match[2] != "" && i != len(segments)-1 {
				problems = append(problems, fmt.Errorf("%s: %q, a {name...} parameter ends the path", at, r.Path))
			}
			parameters[match[1]] = true
			continue
		}
		if !segmentPattern.MatchString(segment) {
			problems = append(problems, fmt.Errorf("%s: path %q, segment %q", at, r.Path, segment))
		}
	}
	switch parameter, deploy := r.DeployParameter(); {
	case !permissionPattern.MatchString(r.Permission):
		problems = append(problems, fmt.Errorf("%s: permission %q", at, r.Permission))
	case deploy && !parameters[parameter]:
		problems = append(problems, fmt.Errorf("%s: permission %q names no parameter of the path", at, r.Permission))
	case deploy && r.EffectiveScope() == ScopeInstance:
		problems = append(problems, fmt.Errorf("%s: a deploy permission belongs to a project's route", at))
	}
	return problems
}

func (p Pipeline) problems(at string) []error {
	var problems []error
	if !capability.ValidName(p.Name) || strings.TrimSpace(p.Label) == "" {
		problems = append(problems, fmt.Errorf("%s: a valid name and a label are required", at))
	}
	if !capability.ValidName(p.PipelineRef) {
		problems = append(problems, fmt.Errorf("%s.pipelineRef %q: a Kubernetes name", at, p.PipelineRef))
	}
	files := append([]string{}, p.Tekton...)
	for _, file := range []string{p.Reading} {
		if file != "" {
			files = append(files, file)
		}
	}
	for _, file := range files {
		if !fs.ValidPath(file) || file == "." {
			problems = append(problems, fmt.Errorf("%s: %q is not a path inside the plugin", at, file))
		}
	}

	results := false
	for _, product := range p.Produces {
		switch product {
		case capability.ProductResults:
			results = true
		case capability.ProductNone:
		default:
			// An image is pushed with a privilege, registry-push, that an installed plugin is not given.
			problems = append(problems, fmt.Errorf("%s.produces: %q, expected results or none", at, product))
		}
	}
	readings := 0
	for _, present := range []bool{p.Reading != "", p.Interpreter != ""} {
		if present {
			readings++
		}
	}
	switch {
	case p.Interpreter != "" && p.Interpreter != InterpreterNative:
		problems = append(problems, fmt.Errorf("%s.interpreter: %q, expected %q", at, p.Interpreter, InterpreterNative))
	case readings > 1:
		problems = append(problems, fmt.Errorf("%s: a reading and an interpreter, the runs are read one way", at))
	case results && readings == 0:
		problems = append(problems, fmt.Errorf("%s: publishes results and says nothing of how they are read", at))
	}

	for name, param := range p.Params {
		switch param.Source {
		case capability.ParamInput, capability.ParamBinding, capability.ParamLiteral:
		default:
			problems = append(problems, fmt.Errorf("%s.params.%s: source %q, expected input, binding or literal", at, name, param.Source))
		}
	}

	declared := map[capability.NeedName]bool{}
	for _, need := range p.Needs {
		switch need.Name {
		case capability.NeedSourceRead, capability.NeedRegistryPull, capability.NeedRegistryPush:
		case capability.NeedClusterDeploy:
			// Asked, never granted here: a service account is bound by the operator's catalogue file
			// only, and a run whose need nobody bound is refused with the reason.
		default:
			problems = append(problems, fmt.Errorf("%s.needs: %q, not a need the dashboard binds", at, need.Name))
		}
		if strings.TrimSpace(need.Reason) == "" {
			problems = append(problems, fmt.Errorf("%s.needs.%s: no reason given", at, need.Name))
		}
		declared[need.Name] = true
	}
	for _, workspace := range p.Workspaces {
		sources := 0
		for _, present := range []bool{workspace.Need != "", workspace.Volume != nil, workspace.EmptyDir} {
			if present {
				sources++
			}
		}
		if workspace.Name == "" || sources != 1 {
			problems = append(problems, fmt.Errorf("%s.workspaces: %q needs a name and exactly one source", at, workspace.Name))
		}
		if workspace.Need != "" && !declared[workspace.Need] {
			problems = append(problems, fmt.Errorf("%s.workspaces.%s: need %q not declared", at, workspace.Name, workspace.Need))
		}
	}

	for _, artifact := range p.Artifacts {
		if artifact.Name == "" || artifact.Param == "" || artifact.Object == "" {
			problems = append(problems, fmt.Errorf("%s.artifacts: name, param and object are required", at))
		}
		if artifact.Format != capability.FormatFile && artifact.Format != capability.FormatArchive {
			problems = append(problems, fmt.Errorf("%s.artifacts.%s: format %q, expected file or archive", at, artifact.Name, artifact.Format))
		}
		for _, member := range artifact.Members {
			if member.Match == "" || member.Parser == "" || member.Tool == "" {
				problems = append(problems, fmt.Errorf("%s.artifacts.%s: a member needs match, parser and tool", at, artifact.Name))
			}
		}
	}
	return problems
}

// providerProblems checks the plugin's tracker, forge, probes and actions.
func (m Manifest) providerProblems() []error {
	var problems []error
	if (m.Tracker || m.Forge) && !m.PerProject {
		problems = append(problems, errors.New("a tracker or a forge is chosen per project: perProject must be set"))
	}
	if len(m.Probes) > 0 && len(m.Events) == 0 {
		problems = append(problems, errors.New("probes report events, and the plugin declares none"))
	}
	probes := map[string]bool{}
	for i, probe := range m.Probes {
		if !capability.ValidName(strings.ReplaceAll(probe.Name, "_", "-")) || probes[probe.Name] {
			problems = append(problems, fmt.Errorf("probes[%d]: %q badly named or declared twice", i, probe.Name))
		}
		probes[probe.Name] = true
		if interval, err := time.ParseDuration(probe.Interval); probe.Interval != "" && (err != nil || interval <= 0) {
			problems = append(problems, fmt.Errorf("probes[%d]: interval %q is not a positive duration", i, probe.Interval))
		}
	}
	actions := map[string]bool{}
	for i, action := range m.Actions {
		at := fmt.Sprintf("actions[%d]", i)
		if !suffixPattern.MatchString(action.Suffix) || strings.TrimSpace(action.Label) == "" || actions[action.Suffix] {
			problems = append(problems, fmt.Errorf("%s: a unique suffix (%q) and a label", at, action.Suffix))
		}
		actions[action.Suffix] = true
		fields := map[string]bool{}
		for j, field := range action.Fields {
			if !fieldPattern.MatchString(field.Name) || strings.TrimSpace(field.Label) == "" || fields[field.Name] {
				problems = append(problems, fmt.Errorf("%s.fields[%d]: a unique name (%q) and a label", at, j, field.Name))
			}
			fields[field.Name] = true
		}
	}
	return problems
}

// Plugin is the plugin as the dashboard registers it, its Tekton definitions, reading and module
// still to be read from its files.
func (m Manifest) Plugin() capability.Plugin {
	plugin := capability.Plugin{
		ID: m.ID, Version: m.Version, APIVersion: m.SDK, Label: m.Label, Summary: m.Summary,
		Global: m.Settings.Global, Project: m.Settings.Project, Events: m.Events, PerProject: m.PerProject,
		Actions: m.Actions,
	}
	if m.Module {
		plugin.Capabilities = []capability.Header{{Kind: capability.KindModule, Name: "module", Label: "Module"}}
	}
	for _, declared := range m.Pipelines {
		plugin.Pipelines = append(plugin.Pipelines, capability.Pipeline{
			Name: declared.Name, Label: declared.Label, Spec: declared.spec(),
		})
	}
	return plugin
}

func (p Pipeline) spec() capability.PipelineSpec {
	spec := capability.PipelineSpec{
		PipelineRef: p.PipelineRef, ExternalTasks: p.ExternalTasks, Produces: p.Produces,
		Verification: p.Verification, Exclusive: p.Exclusive, Group: p.Group,
	}
	if len(p.Params) > 0 {
		spec.Params = make(map[string]capability.Param, len(p.Params))
		for name, param := range p.Params {
			spec.Params[name] = capability.Param{
				Source: param.Source, DefaultKey: param.DefaultKey, Value: param.Value, Required: param.Required,
				Label: param.Label, Help: param.Help, Placeholder: param.Placeholder, Suggest: param.Suggest,
				Image: param.Image,
			}
		}
	}
	for _, workspace := range p.Workspaces {
		converted := capability.Workspace{Name: workspace.Name, Need: workspace.Need, EmptyDir: workspace.EmptyDir}
		if workspace.Volume != nil {
			converted.Volume = &capability.Volume{Storage: workspace.Volume.Storage}
		}
		spec.Workspaces = append(spec.Workspaces, converted)
	}
	for _, need := range p.Needs {
		spec.Needs = append(spec.Needs, capability.Need{Name: need.Name, Reason: need.Reason})
	}
	for _, artifact := range p.Artifacts {
		converted := capability.Artifact{Name: artifact.Name, Param: artifact.Param, Object: artifact.Object, Format: artifact.Format}
		for _, member := range artifact.Members {
			converted.Members = append(converted.Members, capability.Member{
				Match: member.Match, Parser: member.Parser, Tool: member.Tool, MediaType: member.MediaType,
			})
		}
		spec.Artifacts = append(spec.Artifacts, converted)
	}
	return spec
}
