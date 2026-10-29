// Package installed reads a plugin an administrator installs: a directory with its manifest, its
// Tekton definitions, how its runs are read and, for a plugin with a program, that program and what
// it brings, turned into the plugin the registry carries.
//
// The administrator trusts what they install: a plugin's program runs as the dashboard does. What
// stays closed is what a plugin asks of the cluster: its definitions are Pipelines and Tasks only,
// choose no service account, mount nothing of the node and fetch nothing the signature does not
// cover. What it may ask for is closed by the manifest's schema (sdk/manifest).
package installed

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/ui"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/pluginv1"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/providers"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
	uischema "forge.cadoles.com/vcarroy/arkhitekton/sdk/ui"
)

// ReadManifest reads and checks the manifest at the root of a plugin's directory.
func ReadManifest(fsys fs.FS) (manifest.Manifest, error) {
	raw, err := fs.ReadFile(fsys, manifest.FileName)
	if err != nil {
		return manifest.Manifest{}, fmt.Errorf("plugin manifest: %w", err)
	}
	converted, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return manifest.Manifest{}, fmt.Errorf("plugin manifest: %w", err)
	}
	return manifest.Decode(converted)
}

// Plugin is an installed plugin as the dashboard runs it.
type Plugin struct {
	capability.Plugin
	// Native is the plugin's program and what it brings; nil for a plugin without one.
	Native *Native
}

// Native is a plugin's program and what the dashboard does with it.
type Native struct {
	// Manifest says what the program brings: routes, hooks, module, migrations, interface.
	Manifest manifest.Manifest
	// Digest is the program's, `sha256:<hex>`, checked again before each launch.
	Digest string
	// Files is the plugin's directory, the program, migrations and interface included.
	Files fs.FS
	// Interface is what the plugin adds to the dashboard's screens and menus, read from its
	// resources: whether it composes with the others' is checked when it is installed or activated.
	Interface []uischema.Resource
}

// MigrationsTable is the version table of the plugin's migrations, empty when it has none.
func (p Plugin) MigrationsTable() string {
	if p.Native == nil || p.Native.Manifest.Migrations == nil {
		return ""
	}
	return p.Native.Manifest.Migrations.Table
}

// Program opens the plugin's program.
func (n *Native) Program() (io.ReadCloser, error) { return n.Files.Open(n.Manifest.Binary) }

// Programs gives what a plugin's program carries out its stand-ins, which ask the program.
type Programs struct {
	// Interpreter reads the runs of a pipeline the program reads. Nil: this role does not run
	// programs.
	Interpreter func(plugin capability.Plugin, pipeline string) capability.Interpreter
	// Dial reaches the program for its tracker, forge, probes and actions.
	Dial func(plugin capability.Plugin) providers.Dial
}

// Load reads the plugin at the root of fsys and checks it as the registry would load it. A pipeline
// its program reads is checked without the program running.
func Load(fsys fs.FS) (Plugin, error) {
	return load(fsys, manifest.MaxSize)
}

// Read reads the plugin at the root of fsys without compiling how its runs are read: for a plugin
// Load already accepted, about to be compiled once by capabilities.Prepare. What its program carries
// out asks the program through programs.
func Read(fsys fs.FS, programs Programs) (Plugin, error) {
	return read(fsys, manifest.MaxSize, programs)
}

// errNotRunning answers what asks a program no role runs.
var errNotRunning = errors.New("the plugin's program is not running")

// NotRunning stands for the programs of a role that loads the capabilities without running them,
// or of a plugin only checked: what asks the program is refused.
var NotRunning = Programs{
	Interpreter: func(capability.Plugin, string) capability.Interpreter { return notRunning{} },
	Dial: func(capability.Plugin) providers.Dial {
		return func(context.Context) (pluginv1.PluginClient, error) { return nil, errNotRunning }
	},
}

func load(fsys fs.FS, limit int64) (Plugin, error) {
	plugin, err := read(fsys, limit, NotRunning)
	if err != nil {
		return Plugin{}, err
	}
	// The reading compiled, the capabilities composed: a plugin that would not load in the registry
	// is refused now, not at the next reload.
	if _, err := capabilities.New(plugin.Plugin); err != nil {
		return Plugin{}, err
	}
	return plugin, nil
}

// notRunning stands for a program's interpreter while a plugin is only checked.
type notRunning struct{}

func (notRunning) Interpret(context.Context, capability.RunState, []parse.Record) ([]facts.Set, error) {
	return nil, errNotRunning
}

func read(fsys fs.FS, limit int64, programs Programs) (Plugin, error) {
	if err := bounded(fsys, limit); err != nil {
		return Plugin{}, err
	}
	declared, err := ReadManifest(fsys)
	if err != nil {
		return Plugin{}, err
	}

	plugin := declared.Plugin()
	var problems []error
	for i, pipeline := range declared.Pipelines {
		at := fmt.Sprintf("pipeline %s", pipeline.Name)
		definitions, err := definitions(fsys, pipeline)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", at, err))
		}
		plugin.Pipelines[i].Spec.Manifests = definitions
		if pipeline.Reading != "" {
			if plugin.Pipelines[i].Reading, err = fs.ReadFile(fsys, pipeline.Reading); err != nil {
				problems = append(problems, fmt.Errorf("%s: %w", at, err))
			}
		}
		if pipeline.Interpreter == manifest.InterpreterNative {
			if programs.Interpreter == nil {
				problems = append(problems, fmt.Errorf("%s: read by the plugin's program, which this role does not run", at))
			} else {
				plugin.Pipelines[i].Interpreter = programs.Interpreter(plugin, pipeline.Name)
			}
		}
	}
	if programs.Dial != nil {
		attach(&plugin, declared, programs.Dial(plugin))
	}
	native, err := program(fsys, declared)
	if err != nil {
		problems = append(problems, err)
	}
	if err := errors.Join(problems...); err != nil {
		return Plugin{}, err
	}
	return Plugin{Plugin: plugin, Native: native}, nil
}

// attach gives the plugin the tracker, forge, probes and executor its manifest declares, each
// asking the program.
func attach(plugin *capability.Plugin, declared manifest.Manifest, dial providers.Dial) {
	if declared.Tracker {
		plugin.Tracker = &providers.Tracker{Plugin: plugin.ID, Dial: dial}
	}
	if declared.Forge {
		plugin.Forge = providers.Forge{Plugin: plugin.ID, Dial: dial}
	}
	for _, probe := range declared.Probes {
		plugin.Probes = append(plugin.Probes, providers.Probe(plugin.ID, dial, probe.Name, probe.Schedule()))
	}
	if len(declared.Actions) > 0 {
		plugin.Executor = providers.Executor{Plugin: plugin.ID, Dial: dial}
	}
}

// program finds what the plugin's program brings in its directory: the program itself, its
// migrations, its interface.
func program(fsys fs.FS, declared manifest.Manifest) (*Native, error) {
	if declared.Binary == "" {
		return nil, nil
	}
	var problems []error
	file, err := fsys.Open(declared.Binary)
	if err != nil {
		return nil, fmt.Errorf("the plugin's program: %w", err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return nil, fmt.Errorf("the plugin's program: %w", err)
	}
	if migrations := declared.Migrations; migrations != nil {
		if entries, err := fs.ReadDir(fsys, migrations.Dir); err != nil || len(entries) == 0 {
			problems = append(problems, fmt.Errorf("migrations: %s holds no migration", migrations.Dir))
		}
	}
	for _, route := range declared.Routes {
		if _, deploy := route.DeployParameter(); !deploy && !auth.Known(auth.Permission(route.Permission)) {
			problems = append(problems, fmt.Errorf("route %s %s: no role grants %q", route.Method, route.Path, route.Permission))
		}
	}
	var resources []uischema.Resource
	if declaredUI := declared.UI; declaredUI != nil {
		files := declaredUI.Styles
		if declaredUI.Entry != "" {
			files = append([]string{declaredUI.Entry}, files...)
		}
		for _, file := range files {
			if _, err := fs.Stat(fsys, file); err != nil {
				problems = append(problems, fmt.Errorf("ui: %w", err))
			}
		}
		for _, pattern := range declaredUI.Resources {
			if matched, err := fs.Glob(fsys, pattern); err != nil || len(matched) == 0 {
				problems = append(problems, fmt.Errorf("ui.resources: %q matches no file", pattern))
			}
		}
		read, err := ui.ReadFiles(fsys, declaredUI.Resources...)
		if err != nil {
			problems = append(problems, fmt.Errorf("ui.resources: %w", err))
		}
		resources = read
	}
	if err := errors.Join(problems...); err != nil {
		return nil, err
	}
	return &Native{
		Manifest: declared, Digest: "sha256:" + hex.EncodeToString(hash.Sum(nil)), Files: fsys, Interface: resources,
	}, nil
}

// bounded refuses a directory larger than the limit, or holding anything but files and
// directories: a link could name a file outside the plugin.
func bounded(fsys fs.FS, limit int64) error {
	var total int64
	return fs.WalkDir(fsys, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("%s: not a regular file", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if total += info.Size(); total > limit {
			return fmt.Errorf("plugin larger than %d MiB", limit>>20)
		}
		return nil
	})
}

// definitions reads a pipeline's Tekton files and checks them together: exactly one Pipeline, the
// one pipelineRef names, and every Task it runs either shipped or declared external.
func definitions(fsys fs.FS, pipeline manifest.Pipeline) ([]capability.Manifest, error) {
	var problems []error
	var manifests []capability.Manifest
	var pipelines []definition
	tasks := map[string]bool{}
	for _, file := range pipeline.Tekton {
		raw, err := fs.ReadFile(fsys, file)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		read, err := readDefinition(raw)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", file, err))
			continue
		}
		switch read.Kind {
		case "Pipeline":
			pipelines = append(pipelines, read)
		case "Task":
			tasks[read.Name] = true
		}
		manifests = append(manifests, capability.Manifest{Kind: read.Kind, Name: read.Name, YAML: raw})
	}
	if err := errors.Join(problems...); err != nil {
		return nil, err
	}
	// Shipping none, the pipeline is the operator's: the cluster has it, or its runs are refused
	// with the reason.
	if len(pipeline.Tekton) == 0 {
		return nil, nil
	}

	if len(pipelines) != 1 || pipelines[0].Name != pipeline.PipelineRef {
		return nil, fmt.Errorf("expected one Tekton Pipeline, %q, the one pipelineRef names", pipeline.PipelineRef)
	}
	external := map[string]bool{}
	for _, name := range pipeline.ExternalTasks {
		external[name] = true
	}
	for _, name := range pipelines[0].taskRefs() {
		if !tasks[name] && !external[name] {
			problems = append(problems, fmt.Errorf("Task %q neither shipped nor declared external", name))
		}
	}
	return manifests, errors.Join(problems...)
}

// definition is a Tekton definition as far as the checks read it.
type definition struct {
	Kind string
	Name string
	Spec map[string]any
}

// forbidden are the keys a definition may not set, wherever they appear.
var forbidden = map[string]string{
	"serviceAccountName": "a service account is the operator's to choose",
	"serviceAccount":     "a service account is the operator's to choose",
	"hostPath":           "a definition mounts nothing of the node",
	"hostNetwork":        "a definition shares nothing of the node",
	"hostPID":            "a definition shares nothing of the node",
	"hostIPC":            "a definition shares nothing of the node",
	"hostUsers":          "a definition shares nothing of the node",
	"privileged":         "no container runs privileged",
	"resolver":           "a definition fetches nothing its signature does not cover",
	"bundle":             "a definition fetches nothing its signature does not cover",
}

// readDefinition reads one Tekton definition and refuses what would reach beyond its run.
func readDefinition(raw []byte) (definition, error) {
	reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(raw)))
	var object map[string]any
	for {
		document, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return definition{}, err
		}
		converted, err := yaml.YAMLToJSON(document)
		if err != nil {
			return definition{}, err
		}
		if string(bytes.TrimSpace(converted)) == "null" {
			continue
		}
		if object != nil {
			// The installer reads one object per definition: a second one would be checked here and
			// never installed, or the reverse.
			return definition{}, errors.New("one definition per file")
		}
		if err := json.Unmarshal(converted, &object); err != nil {
			return definition{}, err
		}
	}
	if object == nil {
		return definition{}, errors.New("empty")
	}

	apiVersion, _ := object["apiVersion"].(string)
	kind, _ := object["kind"].(string)
	if !strings.HasPrefix(apiVersion, "tekton.dev/") || (kind != "Pipeline" && kind != "Task") {
		return definition{}, fmt.Errorf("%s %s: a plugin ships Tekton Pipelines and Tasks only", apiVersion, kind)
	}
	metadata, _ := object["metadata"].(map[string]any)
	name, _ := metadata["name"].(string)
	if !capability.ValidName(name) {
		return definition{}, fmt.Errorf("%s %q: a Kubernetes name", kind, name)
	}
	if err := refuse(object, kind+" "+name); err != nil {
		return definition{}, err
	}
	spec, _ := object["spec"].(map[string]any)
	return definition{Kind: kind, Name: name, Spec: spec}, nil
}

// refuse walks the whole object: Tekton nests pod templates and inline specs at many depths, and a
// key refused at one depth must be refused at all of them.
func refuse(value any, at string) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			if reason, found := forbidden[key]; found {
				if key != "privileged" || nested == true {
					return fmt.Errorf("%s sets %s: %s", at, key, reason)
				}
			}
			if err := refuse(nested, at); err != nil {
				return err
			}
		}
	case []any:
		for _, nested := range typed {
			if err := refuse(nested, at); err != nil {
				return err
			}
		}
	}
	return nil
}

// taskRefs lists the Tasks a Pipeline runs by name, finally included.
func (d definition) taskRefs() []string {
	var names []string
	for _, section := range []string{"tasks", "finally"} {
		entries, _ := d.Spec[section].([]any)
		for _, entry := range entries {
			task, _ := entry.(map[string]any)
			ref, _ := task["taskRef"].(map[string]any)
			if name, _ := ref["name"].(string); name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

// InterfaceOf reads what a plugin's directory brings to the interface: its resources and the routes
// its blocks may call. Its program is not needed.
func InterfaceOf(fsys fs.FS) (ui.Contribution, error) {
	declared, err := ReadManifest(fsys)
	if err != nil {
		return ui.Contribution{}, err
	}
	contribution := ui.Contribution{Plugin: declared.ID, Routes: declared.Routes}
	if declared.UI == nil || len(declared.UI.Resources) == 0 {
		return contribution, nil
	}
	contribution.Resources, err = ui.ReadFiles(fsys, declared.UI.Resources...)
	return contribution, err
}
