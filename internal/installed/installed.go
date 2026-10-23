// Package installed reads a plugin an administrator installs: a directory with its manifest, its
// Tekton definitions and how its runs are read, turned into the plugin the registry carries.
//
// Everything a compiled plugin is trusted with, an installed one is checked for. Its definitions
// are Pipelines and Tasks only, choose no service account, mount nothing of the node and fetch
// nothing the signature does not cover; its reading is declared or sandboxed. What it may ask for
// is closed by the manifest's schema (sdk/manifest).
package installed

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
)

// Load reads the plugin at the root of fsys and checks it as the registry would load it.
func Load(fsys fs.FS) (capability.Plugin, error) {
	return load(fsys, manifest.MaxSize)
}

// Read reads the plugin at the root of fsys without compiling how its runs are read: for a plugin
// Load already accepted, about to be compiled once by capabilities.Prepare.
func Read(fsys fs.FS) (capability.Plugin, error) {
	return read(fsys, manifest.MaxSize)
}

func load(fsys fs.FS, limit int64) (capability.Plugin, error) {
	plugin, err := read(fsys, limit)
	if err != nil {
		return capability.Plugin{}, err
	}
	// The reading compiled, the module loaded in its sandbox: a plugin that would not load in the
	// registry is refused now, not at the next reload.
	if _, err := capabilities.New(plugin); err != nil {
		return capability.Plugin{}, err
	}
	return plugin, nil
}

func read(fsys fs.FS, limit int64) (capability.Plugin, error) {
	if err := bounded(fsys, limit); err != nil {
		return capability.Plugin{}, err
	}
	raw, err := fs.ReadFile(fsys, manifest.FileName)
	if err != nil {
		return capability.Plugin{}, fmt.Errorf("plugin manifest: %w", err)
	}
	converted, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return capability.Plugin{}, fmt.Errorf("plugin manifest: %w", err)
	}
	declared, err := manifest.Decode(converted)
	if err != nil {
		return capability.Plugin{}, err
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
		if pipeline.Module != "" {
			if plugin.Pipelines[i].Module, err = fs.ReadFile(fsys, pipeline.Module); err != nil {
				problems = append(problems, fmt.Errorf("%s: %w", at, err))
			}
		}
	}
	if err := errors.Join(problems...); err != nil {
		return capability.Plugin{}, err
	}
	return plugin, nil
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
