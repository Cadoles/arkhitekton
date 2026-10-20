// Package capabilities gathers the plugins this binary carries and finds a capability by its ref, by
// the Tekton Pipeline whose runs it reads, or by the producer recorded on a result.
package capabilities

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// ErrVersionDrift refuses to read a run with another interpretation than the one that read it.
var ErrVersionDrift = errors.New("plugin version differs from the one that interpreted the run")

// Registry is the set of capabilities the plugins bring.
type Registry struct {
	plugins   map[string]capability.Plugin
	pipelines map[capability.Ref]capability.Pipeline
	byTekton  map[string]capability.Ref
}

// New gathers plugins and refuses an ambiguous set: two plugins with one ID, two pipelines
// with one name, or two capabilities claiming the same Tekton Pipeline. Ambiguity there would
// decide at random which interpreter reads a run.
func New(plugins ...capability.Plugin) (*Registry, error) {
	registry := &Registry{
		plugins:   make(map[string]capability.Plugin, len(plugins)),
		pipelines: make(map[capability.Ref]capability.Pipeline),
		byTekton:  make(map[string]capability.Ref),
	}

	var problems []error
	for _, plugin := range plugins {
		if !capability.ValidName(plugin.ID) {
			problems = append(problems, fmt.Errorf("plugin %q: lower case, digits and dashes only", plugin.ID))
			continue
		}
		if plugin.Version == "" {
			problems = append(problems, fmt.Errorf("plugin %q: no version", plugin.ID))
		}
		if !compatible(plugin.APIVersion) {
			problems = append(problems, fmt.Errorf("plugin %q: built against sdk %q, this dashboard speaks %q",
				plugin.ID, plugin.APIVersion, sdk.APIVersion))
		}
		if _, taken := registry.plugins[plugin.ID]; taken {
			problems = append(problems, fmt.Errorf("plugin %q declared twice", plugin.ID))
			continue
		}
		registry.plugins[plugin.ID] = plugin

		for _, pipeline := range plugin.Pipelines {
			ref := capability.Ref{Plugin: plugin.ID, Name: pipeline.Name}
			if err := ref.Validate(); err != nil {
				problems = append(problems, err)
				continue
			}
			if _, taken := registry.pipelines[ref]; taken {
				problems = append(problems, fmt.Errorf("capability %s declared twice", ref))
				continue
			}
			registry.pipelines[ref] = pipeline
			if pipeline.Interpreter == nil && produces(pipeline.Spec, capability.ProductResults) {
				problems = append(problems, fmt.Errorf("capability %s publishes results and reads none", ref))
			}

			names := append([]string{pipeline.Spec.PipelineRef}, pipeline.Spec.ObservedPipelines...)
			for _, name := range names {
				if name == "" {
					continue
				}
				if other, taken := registry.byTekton[name]; taken && other != ref {
					problems = append(problems,
						fmt.Errorf("Tekton Pipeline %q claimed by %s and %s", name, other, ref))
					continue
				}
				registry.byTekton[name] = ref
			}
		}
	}
	if err := errors.Join(problems...); err != nil {
		return nil, err
	}
	return registry, nil
}

// Pipeline returns a pipeline capability and the plugin that brings it.
func (r *Registry) Pipeline(ref capability.Ref) (capability.Pipeline, capability.Plugin, bool) {
	pipeline, found := r.pipelines[ref]
	if !found {
		return capability.Pipeline{}, capability.Plugin{}, false
	}
	return pipeline, r.plugins[ref.Plugin], true
}

// ByPipelineRef finds the capability whose runs a Tekton Pipeline produces.
func (r *Registry) ByPipelineRef(name string) (capability.Ref, bool) {
	ref, found := r.byTekton[name]
	return ref, found
}

// Refs lists the pipeline capabilities, sorted.
func (r *Registry) Refs() []capability.Ref {
	refs := make([]capability.Ref, 0, len(r.pipelines))
	for ref := range r.pipelines {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].String() < refs[j].String() })
	return refs
}

// Interpreter returns the interpreter of a capability, provided the plugin is still at the version
// that recorded the run.
func (r *Registry) Interpreter(ref capability.Ref, version string) (capability.Interpreter, error) {
	pipeline, plugin, found := r.Pipeline(ref)
	if !found {
		return nil, fmt.Errorf("capability %s: unknown", ref)
	}
	if plugin.Version != version {
		return nil, fmt.Errorf("capability %s: recorded by %s, installed %s: %w",
			ref, version, plugin.Version, ErrVersionDrift)
	}
	if pipeline.Interpreter == nil {
		return nil, fmt.Errorf("capability %s: no interpreter", ref)
	}
	return pipeline.Interpreter, nil
}

func produces(spec capability.PipelineSpec, product capability.Product) bool {
	for _, declared := range spec.Produces {
		if declared == product {
			return true
		}
	}
	return false
}

// compatible says whether a plugin built against this version of the sdk can be loaded: same major
// version. A minor version only adds to the contract.
func compatible(version string) bool {
	major, _, _ := strings.Cut(version, ".")
	ours, _, _ := strings.Cut(sdk.APIVersion, ".")
	return major != "" && major == ours
}
