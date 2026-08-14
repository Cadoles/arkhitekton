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
	// Workspaces indique, par nom, si le workspace est optionnel.
	Workspaces map[string]bool
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
//  4. **Workspace non lié** — le Pipeline déclare un workspace non optionnel que
//     le catalogue ne fournit pas. Le run échoue en `InvalidWorkspaceBindings`,
//     constaté sur ce cluster : une première version du catalogue ne vérifiait
//     que les paramètres, passait le démarrage, et produisait des runs morts à la
//     seconde. C'est exactement ce que cette validation existe pour empêcher.
//
// Les quatre sont détectables au démarrage. Les laisser passer revient à les
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
		problems = append(problems, entry.validateWorkspaces(spec)...)
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

	// Les URLs d'artefacts sont des paramètres comme les autres, mais elles
	// n'apparaissent pas dans `params` : elles sont minées au lancement. Sans ce
	// contrôle, une faute de frappe dans `uploads[].param` ne se manifesterait
	// qu'au premier run, rejeté par Tekton pour paramètre inconnu.
	for name, upload := range p.Uploads {
		if upload.Param == "" || upload.Artifact == "" {
			problems = append(problems, fmt.Errorf(
				"kind %q: l'artefact %q doit déclarer `param` et `artifact`", p.Kind, name))
			continue
		}
		if _, accepted := spec.Params[upload.Param]; !accepted {
			problems = append(problems, fmt.Errorf(
				"kind %q: le Pipeline %q n'accepte pas le paramètre %q, déclaré pour l'artefact %q (attendus: %s)",
				p.Kind, spec.Name, upload.Param, name, strings.Join(paramNames(spec), ", ")))
		}
	}

	for name, hasDefault := range spec.Params {
		if hasDefault {
			continue
		}
		if _, provided := p.Params[name]; !provided {
			// Un paramètre couvert par un artefact est fourni au lancement.
			if p.coveredByUpload(name) {
				continue
			}
			problems = append(problems, fmt.Errorf(
				"kind %q: le Pipeline %q exige le paramètre %q, que le catalogue ne fournit pas",
				p.Kind, spec.Name, name))
		}
	}

	return problems
}

// coveredByUpload dit si un paramètre reçoit une URL d'artefact.
func (p Pipeline) coveredByUpload(name string) bool {
	for _, upload := range p.Uploads {
		if upload.Param == name {
			return true
		}
	}
	return false
}

func (p Pipeline) validateWorkspaces(spec PipelineSpec) []error {
	var problems []error

	bound := make(map[string]bool, len(p.Workspaces))
	for _, workspace := range p.Workspaces {
		bound[workspace.Name] = true
		if _, declared := spec.Workspaces[workspace.Name]; !declared {
			problems = append(problems, fmt.Errorf(
				"kind %q: le Pipeline %q ne déclare pas le workspace %q (attendus: %s)",
				p.Kind, spec.Name, workspace.Name, strings.Join(workspaceNames(spec), ", ")))
		}
	}

	for name, optional := range spec.Workspaces {
		if optional || bound[name] {
			continue
		}
		problems = append(problems, fmt.Errorf(
			"kind %q: le Pipeline %q exige le workspace %q, que le catalogue ne lie pas",
			p.Kind, spec.Name, name))
	}

	return problems
}

func workspaceNames(spec PipelineSpec) []string {
	list := make([]string, 0, len(spec.Workspaces))
	for name := range spec.Workspaces {
		list = append(list, name)
	}
	sort.Strings(list)
	return list
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
