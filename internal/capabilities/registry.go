// Package capabilities gathers the plugins this binary carries and finds a capability by its ref, by
// the Tekton Pipeline whose runs it reads, or by the producer recorded on a result.
package capabilities

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/declarative"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/wasm"
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
		problems = append(problems, declarationProblems(plugin)...)

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
			// A pipeline reads its runs one way: in code, by declaration or in WebAssembly. The last two
			// become its interpreter here, checked now rather than on the first run they would misread.
			ways := 0
			for _, present := range []bool{pipeline.Interpreter != nil, len(pipeline.Reading) > 0, len(pipeline.Module) > 0} {
				if present {
					ways++
				}
			}
			switch {
			case ways > 1:
				problems = append(problems, fmt.Errorf("capability %s reads its runs more than one way", ref))
			case len(pipeline.Reading) > 0:
				declared, err := declarative.Compile(pipeline.Reading, pipeline.Spec)
				if err != nil {
					problems = append(problems, fmt.Errorf("capability %s: %w", ref, err))
				}
				pipeline.Interpreter = declared
			case len(pipeline.Module) > 0:
				module, err := wasm.Load(context.Background(), ref.String(), pipeline.Module, wasm.DefaultLimits, nil)
				if err != nil {
					problems = append(problems, fmt.Errorf("capability %s: %w", ref, err))
				} else {
					pipeline.Interpreter = module
				}
			}
			registry.pipelines[ref] = pipeline
			if ways == 0 && produces(pipeline.Spec, capability.ProductResults) {
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

// Source gives the registry in force. A registry is its own fixed source; the running dashboard
// hands out one that changes when plugins are installed, and a reader takes it once per
// operation.
type Source interface {
	Current() *Registry
}

// Current makes a registry its own, fixed, source.
func (r *Registry) Current() *Registry { return r }

// Of reads a source that may have been left unset: no source, no registry.
func Of(source Source) *Registry {
	if source == nil {
		return nil
	}
	return source.Current()
}

// ErrUnknown reports an id no plugin carries.
var ErrUnknown = errors.New("unknown plugin")

// Plugins lists the plugins, sorted by identifier.
func (r *Registry) Plugins() []capability.Plugin {
	plugins := make([]capability.Plugin, 0, len(r.plugins))
	for _, plugin := range r.plugins {
		plugins = append(plugins, plugin)
	}
	sort.Slice(plugins, func(i, j int) bool { return plugins[i].ID < plugins[j].ID })
	return plugins
}

// Plugin returns a plugin by identifier. The error names the known plugins: reading the source to
// find out which exist is not an answer to give someone looking at an API message.
func (r *Registry) Plugin(id string) (capability.Plugin, error) {
	if plugin, found := r.plugins[id]; found {
		return plugin, nil
	}
	known := make([]string, 0, len(r.plugins))
	for _, plugin := range r.Plugins() {
		known = append(known, plugin.ID)
	}
	return capability.Plugin{}, fmt.Errorf("%w %q (embedded: %s)", ErrUnknown, id, strings.Join(known, ", "))
}

// FindAction finds the plugin that declares an action reference.
//
// It does not consult whether the plugin is enabled: a plan frozen into a run must stay readable
// after the plugin is switched off, and it is the caller's business whether what it does with the
// answer needs an enabled plugin.
func (r *Registry) FindAction(ref string) (capability.Plugin, capability.ActionSpec, bool) {
	for _, plugin := range r.Plugins() {
		for _, spec := range plugin.Actions {
			if plugin.ActionRef(spec) == ref {
				return plugin, spec, true
			}
		}
	}
	return capability.Plugin{}, capability.ActionSpec{}, false
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

// declarationProblems refuses a plugin whose declaration promises what nothing carries out.
func declarationProblems(plugin capability.Plugin) []error {
	var problems []error
	if len(plugin.Actions) > 0 && plugin.Executor == nil {
		problems = append(problems, fmt.Errorf("plugin %q declares actions and runs none", plugin.ID))
	}
	for _, header := range plugin.Capabilities {
		if header.Kind == capability.KindTracker || header.Kind == capability.KindForge ||
			header.Kind == capability.KindPipeline {
			problems = append(problems, fmt.Errorf(
				"plugin %q: a %s is brought by its implementation, not declared", plugin.ID, header.Kind))
		}
	}
	names := map[string]bool{}
	for _, probe := range plugin.Probes {
		if !capability.ValidName(strings.ReplaceAll(probe.Name(), "_", "-")) || names[probe.Name()] {
			problems = append(problems, fmt.Errorf("plugin %q: probe %q badly named or declared twice",
				plugin.ID, probe.Name()))
		}
		names[probe.Name()] = true
	}
	return problems
}

// PluginProbe is a probe and the plugin that brings it.
type PluginProbe struct {
	Plugin capability.Plugin
	Probe  capability.Probe
}

// Probes lists every plugin's probes.
func (r *Registry) Probes() []PluginProbe {
	var probes []PluginProbe
	for _, plugin := range r.Plugins() {
		for _, probe := range plugin.Probes {
			probes = append(probes, PluginProbe{Plugin: plugin, Probe: probe})
		}
	}
	return probes
}
