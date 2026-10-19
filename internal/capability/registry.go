package capability

import (
	"errors"
	"fmt"
	"sort"
)

// Registry is the set of capabilities the plugins bring.
type Registry struct {
	plugins   map[string]Plugin
	pipelines map[Ref]Pipeline
	byTekton  map[string]Ref
}

// NewRegistry gathers plugins and refuses an ambiguous set: two plugins with one ID, two pipelines
// with one name, or two capabilities claiming the same Tekton Pipeline. Ambiguity there would
// decide at random which interpreter reads a run.
func NewRegistry(plugins ...Plugin) (*Registry, error) {
	registry := &Registry{
		plugins:   make(map[string]Plugin, len(plugins)),
		pipelines: make(map[Ref]Pipeline),
		byTekton:  make(map[string]Ref),
	}

	var problems []error
	for _, plugin := range plugins {
		if !namePattern.MatchString(plugin.ID) {
			problems = append(problems, fmt.Errorf("plugin %q: lower case, digits and dashes only", plugin.ID))
			continue
		}
		if plugin.Version == "" {
			problems = append(problems, fmt.Errorf("plugin %q: no version", plugin.ID))
		}
		if _, taken := registry.plugins[plugin.ID]; taken {
			problems = append(problems, fmt.Errorf("plugin %q declared twice", plugin.ID))
			continue
		}
		registry.plugins[plugin.ID] = plugin

		for _, pipeline := range plugin.Pipelines {
			ref := Ref{Plugin: plugin.ID, Name: pipeline.Name}
			if err := ref.Validate(); err != nil {
				problems = append(problems, err)
				continue
			}
			if _, taken := registry.pipelines[ref]; taken {
				problems = append(problems, fmt.Errorf("capability %s declared twice", ref))
				continue
			}
			registry.pipelines[ref] = pipeline
			if pipeline.Interpreter == nil && produces(pipeline.Spec, ProductResults) {
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
func (r *Registry) Pipeline(ref Ref) (Pipeline, Plugin, bool) {
	pipeline, found := r.pipelines[ref]
	if !found {
		return Pipeline{}, Plugin{}, false
	}
	return pipeline, r.plugins[ref.Plugin], true
}

// ByPipelineRef finds the capability whose runs a Tekton Pipeline produces.
func (r *Registry) ByPipelineRef(name string) (Ref, bool) {
	ref, found := r.byTekton[name]
	return ref, found
}

// Refs lists the pipeline capabilities, sorted.
func (r *Registry) Refs() []Ref {
	refs := make([]Ref, 0, len(r.pipelines))
	for ref := range r.pipelines {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].String() < refs[j].String() })
	return refs
}

// Interpreter returns the interpreter of a capability, provided the plugin is still at the version
// that recorded the run.
func (r *Registry) Interpreter(ref Ref, version string) (Interpreter, error) {
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

func produces(spec PipelineSpec, product Product) bool {
	for _, declared := range spec.Produces {
		if declared == product {
			return true
		}
	}
	return false
}
