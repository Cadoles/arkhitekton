package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// PipelineSpec est ce que le catalogue a besoin de savoir d'un Pipeline du
// cluster : son nom et ses paramètres.
//
// Un type propre plutôt que celui de Tekton, pour que ce paquet ne dépende pas de
// client-go — et surtout pour que la validation soit testable sans cluster.
type PipelineSpec struct {
	Name string
	// Params indique, par nom, si le paramètre a une valeur par défaut.
	Params map[string]bool
}

// Cluster fournit les Pipeline réellement déployés.
type Cluster interface {
	Pipelines(ctx context.Context) (map[string]PipelineSpec, error)
}

// ValidateAgainst confronte le catalogue au cluster.
//
// Trois écarts sont recherchés, et chacun correspond à une panne observée sur ce
// genre de chaîne :
//
//  1. **Pipeline absent** — le catalogue référence un nom qui n'existe pas. Le run
//     serait créé puis échouerait immédiatement en `CouldntGetPipeline`, sans que
//     le message désigne le catalogue.
//  2. **Paramètre inconnu** — le catalogue fournit un paramètre que le Pipeline
//     n'accepte pas. Tekton refuse le run entier.
//  3. **Paramètre requis non couvert** — le Pipeline attend un paramètre sans
//     valeur par défaut que le catalogue ne fournit pas. Le run échoue en
//     `ParameterMissing`, motif qui a déjà piégé un outil de suivi de ce projet.
//
// Les trois sont détectables au démarrage. Les laisser passer revient à les
// découvrir au premier lancement, c'est-à-dire au pire moment.
func (c *Catalog) ValidateAgainst(ctx context.Context, cluster Cluster) error {
	deployed, err := cluster.Pipelines(ctx)
	if err != nil {
		return fmt.Errorf("lecture des Pipeline du cluster: %w", err)
	}

	var problems []error
	for _, entry := range c.Pipelines {
		spec, present := deployed[entry.PipelineRef]
		if !present {
			problems = append(problems, fmt.Errorf(
				"kind %q: le Pipeline %q est absent du cluster (déployés: %s)",
				entry.Kind, entry.PipelineRef, strings.Join(names(deployed), ", ")))
			continue
		}
		problems = append(problems, entry.validateParams(spec)...)
	}

	return errors.Join(problems...)
}

func (p Pipeline) validateParams(spec PipelineSpec) []error {
	var problems []error

	for name := range p.Params {
		if _, accepted := spec.Params[name]; !accepted {
			problems = append(problems, fmt.Errorf(
				"kind %q: le Pipeline %q n'accepte pas le paramètre %q (attendus: %s)",
				p.Kind, spec.Name, name, strings.Join(paramNames(spec), ", ")))
		}
	}

	for name, hasDefault := range spec.Params {
		if hasDefault {
			continue
		}
		if _, provided := p.Params[name]; !provided {
			problems = append(problems, fmt.Errorf(
				"kind %q: le Pipeline %q exige le paramètre %q, que le catalogue ne fournit pas",
				p.Kind, spec.Name, name))
		}
	}

	return problems
}

func names(deployed map[string]PipelineSpec) []string {
	list := make([]string, 0, len(deployed))
	for name := range deployed {
		list = append(list, name)
	}
	sort.Strings(list)
	return list
}

func paramNames(spec PipelineSpec) []string {
	list := make([]string, 0, len(spec.Params))
	for name := range spec.Params {
		list = append(list, name)
	}
	sort.Strings(list)
	return list
}
