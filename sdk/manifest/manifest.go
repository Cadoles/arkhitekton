// Package manifest is the schema of a plugin an instance installs without recompiling.
//
// Such a plugin is a directory: `arkhitekton-plugin.yaml` at its root, the Tekton definitions of its
// pipelines, and for each pipeline either a declared reading (see the declare package) or a module
// compiled to WebAssembly (see the guest package). The dashboard converts the YAML to JSON and reads
// it with this package.
//
// An installed plugin brings pipeline capabilities only. A tracker, a forge, a probe, an action or a
// module is code the dashboard runs with its own privileges: those remain for plugins compiled with
// it.
package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// APIVersion is the schema version a manifest states.
const APIVersion = "arkhitekton.plugin/v1"

// FileName is the manifest's name at the root of the plugin's directory.
const FileName = "arkhitekton-plugin.yaml"

// MaxSize bounds a plugin's directory, every file counted.
const MaxSize = 64 << 20

// A plugin is distributed as an OCI artifact: an empty config of ConfigMediaType, and one layer of
// LayerMediaType, the directory as a gzipped tar.
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
	SDK       string     `json:"sdk"`
	Label     string     `json:"label"`
	Summary   string     `json:"summary,omitempty"`
	Settings  Settings   `json:"settings,omitzero"`
	Pipelines []Pipeline `json:"pipelines"`
}

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
	Tekton []string `json:"tekton"`
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
	// Reading or Module, never both: the file that says how the runs are read.
	Reading string `json:"reading,omitempty"`
	Module  string `json:"module,omitempty"`
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

// versionPattern keeps a version usable as a path segment, where it is stored.
var versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.-]*$`)

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
	if m.APIVersion != APIVersion {
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
	if len(m.Pipelines) == 0 {
		problems = append(problems, errors.New("no pipeline: an installed plugin brings pipelines"))
	}
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
	}
	return errors.Join(problems...)
}

func (p Pipeline) problems(at string) []error {
	var problems []error
	if !capability.ValidName(p.Name) || strings.TrimSpace(p.Label) == "" {
		problems = append(problems, fmt.Errorf("%s: a valid name and a label are required", at))
	}
	if !capability.ValidName(p.PipelineRef) {
		problems = append(problems, fmt.Errorf("%s.pipelineRef %q: a Kubernetes name", at, p.PipelineRef))
	}
	if len(p.Tekton) == 0 {
		problems = append(problems, fmt.Errorf("%s: no Tekton definition", at))
	}
	files := append([]string{}, p.Tekton...)
	for _, file := range []string{p.Reading, p.Module} {
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
	switch {
	case p.Reading != "" && p.Module != "":
		problems = append(problems, fmt.Errorf("%s: reading and module, the runs are read one way", at))
	case results && p.Reading == "" && p.Module == "":
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
		case capability.NeedSourceRead, capability.NeedRegistryPull:
		default:
			// Pushing an image and deploying act on the instance's behalf: a binding from the screen
			// cannot grant them, and an installed plugin cannot ask for them.
			problems = append(problems, fmt.Errorf("%s.needs: %q, expected source-read or registry-pull", at, need.Name))
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

// Plugin is the plugin as the dashboard registers it, its Tekton definitions, reading and module
// still to be read from its files.
func (m Manifest) Plugin() capability.Plugin {
	plugin := capability.Plugin{
		ID: m.ID, Version: m.Version, APIVersion: m.SDK, Label: m.Label, Summary: m.Summary,
		Global: m.Settings.Global, Project: m.Settings.Project,
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
				Label: param.Label, Help: param.Help, Placeholder: param.Placeholder,
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
