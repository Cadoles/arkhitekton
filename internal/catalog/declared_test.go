package catalog

import "sigs.k8s.io/yaml"

// parseDeclared builds a catalogue from pipelines written whole, the form bindings resolve to.
//
// The file format no longer accepts them: a pipeline enters only through a plugin. The tests of
// what the catalogue checks on a pipeline write it directly, which is shorter than a plugin per
// case and checks the same thing.
func parseDeclared(content []byte) (*Catalog, error) {
	var source struct {
		Pipelines         []Pipeline                    `json:"pipelines"`
		Projects          map[string]ProjectDeclaration `json:"projects,omitempty"`
		Score             map[string]float64            `json:"score,omitempty"`
		MaxConcurrentRuns int                           `json:"maxConcurrentRuns"`
		PodTemplate       *PodTemplate                  `json:"podTemplate,omitempty"`
	}
	if err := yaml.UnmarshalStrict(content, &source); err != nil {
		return nil, err
	}
	declared := &Catalog{
		Pipelines: source.Pipelines, Projects: source.Projects, Score: source.Score,
		MaxConcurrentRuns: source.MaxConcurrentRuns, PodTemplate: source.PodTemplate,
	}
	if declared.MaxConcurrentRuns == 0 {
		declared.MaxConcurrentRuns = DefaultMaxConcurrentRuns
	}
	if err := declared.validate(); err != nil {
		return nil, err
	}
	return declared, nil
}
