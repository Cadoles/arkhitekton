// Package capability describes what a plugin brings: typed capabilities, and for pipelines, the
// Tekton definitions, what they publish, what they need and how their runs are read.
//
// A plugin offers; the operator's catalogue instantiates; a project's plan composes. This package
// is the first of the three. It says nothing about service accounts or secret names: a capability
// declares a [Need], and only the operator decides what satisfies it.
//
// This package imports only the other two contract packages, facts and parse.
package capability

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/parse"
)

// Kind is what a capability is. It qualifies a capability, not a plugin: one plugin may bring a
// pipeline and a tracker.
type Kind string

const (
	KindPipeline Kind = "pipeline"
	KindTracker  Kind = "tracker"
	KindForge    Kind = "forge"
	KindExporter Kind = "exporter"
	KindModule   Kind = "module"
)

var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Ref names a capability within its plugin: `php/unit-tests`. Qualified, so that two plugins cannot
// collide.
type Ref struct {
	Plugin string
	Name   string
}

// ParseRef reads `plugin/name`.
func ParseRef(raw string) (Ref, error) {
	plugin, name, found := strings.Cut(strings.TrimSpace(raw), "/")
	ref := Ref{Plugin: plugin, Name: name}
	if !found {
		return Ref{}, fmt.Errorf("capability %q: expected plugin/name", raw)
	}
	if err := ref.Validate(); err != nil {
		return Ref{}, err
	}
	return ref, nil
}

// Validate checks both segments: lower case, digits and inner dashes.
func (r Ref) Validate() error {
	if !namePattern.MatchString(r.Plugin) || !namePattern.MatchString(r.Name) {
		return fmt.Errorf("capability %q: lower case, digits and dashes only", r.String())
	}
	return nil
}

func (r Ref) String() string { return r.Plugin + "/" + r.Name }

// Slug is the ref as a Kubernetes name segment, where `/` is not allowed.
func (r Ref) Slug() string { return r.Plugin + "-" + r.Name }

// LabelValue is the ref as a Kubernetes label value, where `/` is not allowed either. A dot, not a
// dash: `php-unit.tests` and `php.unit-tests` must not both read `php-unit-tests`.
func (r Ref) LabelValue() string { return r.Plugin + "." + r.Name }

// Plugin is what a plugin brings.
type Plugin struct {
	ID string
	// Version changes whenever interpretation changes. It is recorded on every stored result, and a
	// replay refuses to read a run with another version than the one that read it first.
	Version   string
	Pipelines []Pipeline
}

// Pipeline is a pipeline capability.
type Pipeline struct {
	Name        string
	Label       string
	Spec        PipelineSpec
	Interpreter Interpreter
}

// Product is what a pipeline leaves behind.
type Product string

const (
	ProductImage   Product = "image"
	ProductResults Product = "results"
	ProductNone    Product = "none"
)

// PipelineSpec is what the pipeline is, independently of who runs it.
type PipelineSpec struct {
	// PipelineRef is the Tekton Pipeline's name.
	PipelineRef string
	// Manifests are the Pipeline and its Tasks, installed by the dashboard. Empty when the operator
	// provides them.
	Manifests []Manifest
	// ExternalTasks are Tasks the pipeline uses without bringing them; their presence is checked.
	ExternalTasks []string
	Params        map[string]Param
	Workspaces    []Workspace
	Artifacts     []Artifact
	Needs         []Need
	Produces      []Product
	// Verification is the default: a successful build chains this pipeline. The catalogue may
	// override it.
	Verification bool
	// Exclusive runs one at a time by default.
	Exclusive bool
	// ObservedPipelines are Tekton Pipelines whose runs this capability reads although the dashboard
	// did not launch them.
	ObservedPipelines []string
	// Group is where the pipeline's results are shown before any is read: a step announced in a
	// plan has a place on screen from the start. The interpreter decides the group of what it reads.
	Group string
}

// Manifest is a Tekton definition shipped with the plugin.
type Manifest struct {
	Kind string
	Name string
	YAML []byte
}

// ParamSource says who gives a parameter its value.
type ParamSource string

const (
	// ParamInput: given at launch, by a plan step or a person.
	ParamInput ParamSource = "input"
	// ParamBinding: read from the operator's configuration, under the key the catalogue binds.
	ParamBinding ParamSource = "binding"
	// ParamLiteral: fixed by the capability.
	ParamLiteral ParamSource = "literal"
)

// Param describes a Pipeline parameter.
type Param struct {
	Source ParamSource
	// DefaultKey is the configuration key read when the catalogue binds none.
	DefaultKey  string
	Value       string
	Required    bool
	Suggest     string
	Image       string
	Label       string
	Help        string
	Placeholder string
}

// Workspace is a Pipeline workspace. Exactly one source.
type Workspace struct {
	Name string
	// Need is set for a secret-backed workspace: the operator binds the secret to the need.
	Need NeedName
	// Volume asks for a claim per run. The operator chooses its storage class and may resize it.
	Volume   *Volume
	EmptyDir bool
}

// Volume is a claim created for each run.
type Volume struct {
	// Storage is the default size, `1Gi`.
	Storage string
}

// ArtifactFormat says how an artifact is packed.
type ArtifactFormat string

const (
	FormatFile    ArtifactFormat = "file"
	FormatArchive ArtifactFormat = "archive"
)

// Artifact is something the pipeline uploads to the presigned URL it is given.
type Artifact struct {
	Name string
	// Param receives the presigned URL.
	Param string
	// Object is the object's name under the run's prefix.
	Object  string
	Format  ArtifactFormat
	Members []Member
}

// Member says how to read a file of an artifact. Match is a path pattern inside the archive, or the
// file's own name.
type Member struct {
	Match  string
	Parser parse.ID
	Tool   string
}

// NeedName is a privilege a pipeline requires. The set is closed: the operator must be able to
// read, from the name alone, what granting it means.
type NeedName string

const (
	NeedSourceRead    NeedName = "source-read"
	NeedRegistryPull  NeedName = "registry-pull"
	NeedRegistryPush  NeedName = "registry-push"
	NeedClusterDeploy NeedName = "cluster-deploy"
)

// Need is a privilege a pipeline declares, with why it needs it.
type Need struct {
	Name   NeedName
	Reason string
}

// Interpreter reads a finished run.
//
// It receives the run's state as Tekton reported it and the parsed artifacts, and returns facts. It
// does no I/O. The host validates what it returns and stamps the producer: an interpreter cannot
// claim to be another plugin.
type Interpreter interface {
	Interpret(ctx context.Context, run RunState, records []parse.Record) ([]facts.Set, error)
}

// RunState is a finished run as Tekton reported it.
type RunState struct {
	Status      string
	Reason      string
	StartedAt   *time.Time
	CompletedAt *time.Time
	Tasks       []TaskState
	// Conditions are the queue's at launch. A measure taken alongside other runs is not
	// comparable with a baseline taken alone.
	Conditions Conditions
}

// TaskState is one task of the run. Name is the pipeline's task name, stable across runs.
type TaskState struct {
	Name   string
	Status string
	Reason string
	Steps  []StepState
}

// StepState is one step. ExitCode is nil when the container never terminated.
type StepState struct {
	Name     string
	Status   string
	ExitCode *int32
}

// Conditions are the queue's state when the run started.
type Conditions struct {
	Exclusive         bool
	ConcurrentAtStart int
}

// Step finds a step by task and step name.
func (s RunState) Step(task, step string) (StepState, bool) {
	for _, t := range s.Tasks {
		if t.Name != task {
			continue
		}
		for _, candidate := range t.Steps {
			if candidate.Name == step {
				return candidate, true
			}
		}
	}
	return StepState{}, false
}

// ErrVersionDrift refuses to read a run with another interpretation than the one that read it.
var ErrVersionDrift = errors.New("plugin version differs from the one that interpreted the run")

// KindLabel names each kind for the interface.
var KindLabel = map[Kind]string{
	KindPipeline: "Pipeline",
	KindTracker:  "Suivi",
	KindForge:    "Forge",
	KindExporter: "Livrables",
	KindModule:   "Module",
}

// Header names a capability without describing it: what a plugin's descriptor lists.
type Header struct {
	Kind  Kind
	Name  string
	Label string
}
