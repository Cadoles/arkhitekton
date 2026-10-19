package catalog

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capability"
)

/*
 * Binding capabilities.
 *
 * A plugin says what a pipeline is: its Tekton definition, its parameters, what it publishes and
 * what it needs. The catalogue says how this instance runs it: which configuration key fills a
 * parameter, which secret satisfies a need, which service account deploys, how long it may run.
 * The catalogue never describes a pipeline any more, and a plugin never chooses a privilege.
 *
 * A binding is resolved into the same [Pipeline] the rest of the dashboard already reads, so the
 * scheduler and the PipelineRun builder do not change.
 */

// Binding is how the operator instantiates a capability.
type Binding struct {
	// Ref is the capability, `plugin/name`.
	Ref string `json:"ref"`
	// Verification overrides the capability's default.
	Verification *bool `json:"verification,omitempty"`
	// MaxConcurrent overrides the capability's default ceiling.
	MaxConcurrent int    `json:"maxConcurrent,omitempty"`
	Timeout       string `json:"timeout,omitempty"`
	// Params bind the capability's `binding` parameters to configuration keys. Unbound, a
	// parameter reads the capability's default key.
	Params map[string]ParamBinding `json:"params,omitempty"`
	// Needs say what satisfies each privilege the capability declares.
	Needs map[string]NeedBinding `json:"needs,omitempty"`
	// Volumes size the per-run claims.
	Volumes     map[string]VolumeBinding `json:"volumes,omitempty"`
	PodTemplate *PodTemplate             `json:"podTemplate,omitempty"`
}

// ParamBinding names the configuration key that fills a parameter.
type ParamBinding struct {
	Key string `json:"key"`
}

// NeedBinding satisfies a need: a secret for the needs a workspace carries, a service account for
// deploying. Only `cluster-deploy` accepts a service account: it is the one need that is a cluster
// identity, and naming an account anywhere else would let a binding grant more than it says.
type NeedBinding struct {
	Secret             *SecretSource `json:"secret,omitempty"`
	ServiceAccountName string        `json:"serviceAccountName,omitempty"`
}

// VolumeBinding sizes a per-run claim.
type VolumeBinding struct {
	StorageClassName string   `json:"storageClassName,omitempty"`
	Storage          string   `json:"storage,omitempty"`
	AccessModes      []string `json:"accessModes,omitempty"`
}

// Option configures parsing.
type Option func(*parseOptions)

type parseOptions struct {
	registry *capability.Registry
}

// WithCapabilities resolves the catalogue's bindings against the plugins' capabilities.
func WithCapabilities(registry *capability.Registry) Option {
	return func(options *parseOptions) { options.registry = registry }
}

// legacyKinds are the kinds the catalogue named pipelines by before capabilities. Kept as the
// pipelines' identity until references are qualified everywhere.
var legacyKinds = map[string]string{
	"core/build":              "build",
	"core/retag":              "retag",
	"php/static-tests":        "static-tests",
	"php/unit-tests":          "unit-tests",
	"mse/integration-tests":   "integration-tests",
	"mse/e2e-tests":           "e2e-tests",
	"security/security-audit": "security-audit",
	"k6/load":                 "load-tests",
}

// resolve turns the bindings into pipelines.
func (c *Catalog) resolve(registry *capability.Registry) error {
	if len(c.Capabilities) == 0 {
		return nil
	}
	if registry == nil {
		return errors.New("the catalogue binds capabilities, but no plugin registry was given")
	}

	var problems []error
	for _, binding := range c.Capabilities {
		pipeline, err := resolveBinding(registry, binding)
		if err != nil {
			problems = append(problems, fmt.Errorf("capability %q: %w", binding.Ref, err))
			continue
		}
		c.Pipelines = append(c.Pipelines, pipeline)
	}
	return errors.Join(problems...)
}

func resolveBinding(registry *capability.Registry, binding Binding) (Pipeline, error) {
	ref, err := capability.ParseRef(binding.Ref)
	if err != nil {
		return Pipeline{}, err
	}
	declared, _, found := registry.Pipeline(ref)
	if !found {
		return Pipeline{}, errors.New("no plugin brings it")
	}
	kind, known := legacyKinds[ref.String()]
	if !known {
		return Pipeline{}, errors.New("not yet launchable: pipelines are still named by their legacy kind")
	}
	spec := declared.Spec
	if spec.PipelineRef == "" {
		return Pipeline{}, errors.New("an observed capability is not launched, and has nothing to bind")
	}

	pipeline := Pipeline{
		Kind:          kind,
		Capability:    ref.String(),
		PipelineRef:   spec.PipelineRef,
		Label:         declared.Label,
		Verification:  spec.Verification,
		MaxConcurrent: binding.MaxConcurrent,
		Timeout:       binding.Timeout,
		PodTemplate:   binding.PodTemplate,
	}
	if binding.Verification != nil {
		pipeline.Verification = *binding.Verification
	}
	if pipeline.MaxConcurrent == 0 && spec.Exclusive {
		pipeline.MaxConcurrent = ExclusiveKindConcurrency
	}
	for _, product := range spec.Produces {
		pipeline.Produces = append(pipeline.Produces, Product(product))
	}

	var problems []error
	problems = append(problems, bindParams(&pipeline, spec, binding)...)
	problems = append(problems, bindNeeds(&pipeline, spec, binding)...)
	problems = append(problems, bindWorkspaces(&pipeline, spec, binding)...)

	for _, artifact := range spec.Artifacts {
		if artifact.Param == "" {
			continue
		}
		if pipeline.Uploads == nil {
			pipeline.Uploads = map[string]Upload{}
		}
		pipeline.Uploads[artifact.Name] = Upload{
			Param: artifact.Param, Artifact: artifact.Object,
			Family: spec.Group, Format: string(artifact.Format),
		}
	}
	return pipeline, errors.Join(problems...)
}

func bindParams(pipeline *Pipeline, spec capability.PipelineSpec, binding Binding) []error {
	var problems []error
	for name := range binding.Params {
		declared, found := spec.Params[name]
		switch {
		case !found:
			problems = append(problems, fmt.Errorf("param %q is not declared by the capability", name))
		case declared.Source != capability.ParamBinding:
			problems = append(problems, fmt.Errorf("param %q is not read from configuration", name))
		}
	}

	if len(spec.Params) > 0 {
		pipeline.Params = make(map[string]Param, len(spec.Params))
	}
	for name, declared := range spec.Params {
		param := Param{
			Required: declared.Required, Suggest: declared.Suggest, Image: declared.Image,
			Label: declared.Label, Help: declared.Help, Placeholder: declared.Placeholder,
		}
		switch declared.Source {
		case capability.ParamBinding:
			param.Source, param.Key = SourceConfig, declared.DefaultKey
			if bound, found := binding.Params[name]; found && strings.TrimSpace(bound.Key) != "" {
				param.Key = bound.Key
			}
			if param.Key == "" {
				pipeline.unavailable(fmt.Sprintf("param %s is bound to no configuration key", name))
			}
		case capability.ParamLiteral:
			param.Source, param.Value = SourceLiteral, declared.Value
		default:
			param.Source = SourceInput
		}
		pipeline.Params[name] = param
	}
	return problems
}

func bindNeeds(pipeline *Pipeline, spec capability.PipelineSpec, binding Binding) []error {
	declared := map[string]bool{}
	for _, need := range spec.Needs {
		declared[string(need.Name)] = true
	}

	var problems []error
	names := make([]string, 0, len(binding.Needs))
	for name := range binding.Needs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		bound := binding.Needs[name]
		if !declared[name] {
			problems = append(problems, fmt.Errorf("need %q is not declared by the capability", name))
			continue
		}
		if bound.ServiceAccountName != "" && name != string(capability.NeedClusterDeploy) {
			problems = append(problems, fmt.Errorf(
				"need %q: a service account satisfies only %q", name, capability.NeedClusterDeploy))
		}
	}

	for _, need := range spec.Needs {
		bound, found := binding.Needs[string(need.Name)]
		switch {
		case need.Name == capability.NeedClusterDeploy:
			if !found || bound.ServiceAccountName == "" {
				pipeline.unavailable(fmt.Sprintf("need %s is bound to no service account", need.Name))
				continue
			}
			pipeline.ServiceAccountName = bound.ServiceAccountName
		case !found || bound.Secret == nil:
			pipeline.unavailable(fmt.Sprintf("need %s is bound to no secret", need.Name))
		}
	}
	return problems
}

func bindWorkspaces(pipeline *Pipeline, spec capability.PipelineSpec, binding Binding) []error {
	var problems []error
	for name := range binding.Volumes {
		var found bool
		for _, workspace := range spec.Workspaces {
			found = found || (workspace.Name == name && workspace.Volume != nil)
		}
		if !found {
			problems = append(problems, fmt.Errorf("volume %q is not declared by the capability", name))
		}
	}

	for _, declared := range spec.Workspaces {
		workspace := Workspace{Name: declared.Name, EmptyDir: declared.EmptyDir}
		switch {
		case declared.Need != "":
			if bound, found := binding.Needs[string(declared.Need)]; found && bound.Secret != nil {
				secret := *bound.Secret
				workspace.Secret = &secret
			}
		case declared.Volume != nil:
			volume := binding.Volumes[declared.Name]
			claim := &VolumeClaimTemplate{
				AccessModes:      volume.AccessModes,
				StorageClassName: volume.StorageClassName,
				Storage:          declared.Volume.Storage,
			}
			if len(claim.AccessModes) == 0 {
				claim.AccessModes = []string{"ReadWriteOnce"}
			}
			if volume.Storage != "" {
				claim.Storage = volume.Storage
			}
			workspace.VolumeClaimTemplate = claim
		}
		pipeline.Workspaces = append(pipeline.Workspaces, workspace)
	}
	return problems
}

// unavailable records why the pipeline cannot run. It stays in the catalogue: a plan may name it,
// and its runs are refused with this reason rather than failing in the cluster.
func (p *Pipeline) unavailable(reason string) {
	if p.Unavailable == "" {
		p.Unavailable = reason
		return
	}
	p.Unavailable += "; " + reason
}
