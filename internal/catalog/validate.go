package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// PipelineSpec is what the catalogue needs to know about a Pipeline in the
// cluster: its name and its parameters.
//
// A local type rather than Tekton's, so this package does not depend on client-go
// — and above all so validation is testable without a cluster.
type PipelineSpec struct {
	Name string
	// Params says, by name, whether the parameter has a default.
	Params map[string]bool
	// Workspaces says, by name, whether the workspace is optional.
	Workspaces map[string]bool
}

// Cluster supplies the Pipelines and Tasks actually deployed.
type Cluster interface {
	Pipelines(ctx context.Context) (map[string]PipelineSpec, error)
	// Tasks returns the names of the deployed Tasks.
	Tasks(ctx context.Context) (map[string]bool, error)
}

// ValidateAgainst checks the catalogue against the cluster.
//
// Four mismatches are looked for, each matching an observed failure:
//
//  1. Missing Pipeline — the catalogue references a name that does not exist. The
//     run would be created and fail immediately with CouldntGetPipeline, without
//     the message naming the catalogue.
//  2. Unknown parameter — the catalogue supplies a parameter the Pipeline does not
//     accept. Tekton refuses the whole run.
//  3. Required parameter not covered — the Pipeline expects a parameter with no
//     default that the catalogue does not supply. The run fails with
//     ParameterMissing.
//  4. Unbound workspace — the Pipeline declares a non-optional workspace the
//     catalogue does not supply. The run fails with InvalidWorkspaceBindings,
//     observed on this cluster: an early catalogue checked only parameters, passed
//     start-up, and produced runs that died within a second.
//
// All four are detectable at start-up. Letting them pass means discovering them at
// the first launch, which is the worst moment.
func (c *Catalog) ValidateAgainst(ctx context.Context, cluster Cluster) error {
	unavailable, err := c.Availability(ctx, cluster)
	if err != nil {
		return err
	}
	var problems []error
	for _, entry := range c.Pipelines {
		if reason, found := unavailable[entry.Kind]; found {
			problems = append(problems, fmt.Errorf("kind %q: %s", entry.Kind, reason))
		}
	}
	return errors.Join(problems...)
}

// Availability checks each pipeline against the cluster and returns, by kind, why the ones that
// cannot run cannot. A pipeline at odds with the cluster no longer stops the others: it is refused
// on its own, with its reason, while the rest of the catalogue keeps running.
//
// A Task the pipeline uses without shipping it must be deployed too: without it the run starts and
// fails at the step that needs it.
func (c *Catalog) Availability(ctx context.Context, cluster Cluster) (map[string]string, error) {
	deployed, err := cluster.Pipelines(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the cluster's Pipelines: %w", err)
	}
	tasks, err := cluster.Tasks(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the cluster's Tasks: %w", err)
	}

	unavailable := map[string]string{}
	for _, entry := range c.Pipelines {
		var problems []error
		spec, present := deployed[entry.PipelineRef]
		if !present {
			problems = append(problems, fmt.Errorf("Pipeline %q is absent from the cluster (deployed: %s)",
				entry.PipelineRef, strings.Join(names(deployed), ", ")))
		} else {
			problems = append(problems, entry.validateParams(spec)...)
			problems = append(problems, entry.validateWorkspaces(spec)...)
		}
		for _, task := range entry.ExternalTasks {
			if !tasks[task] {
				problems = append(problems, fmt.Errorf("Task %q is absent from the cluster", task))
			}
		}
		if len(problems) > 0 {
			reasons := make([]string, 0, len(problems))
			for _, problem := range problems {
				reasons = append(reasons, problem.Error())
			}
			unavailable[entry.Kind] = strings.Join(reasons, "; ")
		}
	}
	return unavailable, nil
}

func (p Pipeline) validateParams(spec PipelineSpec) []error {
	var problems []error

	for name := range p.Params {
		if _, accepted := spec.Params[name]; !accepted {
			problems = append(problems, fmt.Errorf(
				"kind %q: Pipeline %q does not accept parameter %q (expected: %s)",
				p.Kind, spec.Name, name, strings.Join(paramNames(spec), ", ")))
		}
	}

	// Artifact URLs are parameters like any other, but they do not appear in `params`:
	// they are minted at launch. Without this check, a typo in `uploads[].param` would
	// only surface at the first run, rejected by Tekton as an unknown parameter.
	for name, upload := range p.Uploads {
		if upload.Param == "" || upload.Artifact == "" {
			problems = append(problems, fmt.Errorf(
				"kind %q: artifact %q must declare `param` and `artifact`", p.Kind, name))
			continue
		}
		if _, accepted := spec.Params[upload.Param]; !accepted {
			problems = append(problems, fmt.Errorf(
				"kind %q: Pipeline %q does not accept parameter %q, declared for artifact %q (expected: %s)",
				p.Kind, spec.Name, upload.Param, name, strings.Join(paramNames(spec), ", ")))
		}
	}

	for name, hasDefault := range spec.Params {
		if hasDefault {
			continue
		}
		if _, provided := p.Params[name]; !provided {
			// A parameter covered by an upload is supplied at launch.
			if p.coveredByUpload(name) {
				continue
			}
			problems = append(problems, fmt.Errorf(
				"kind %q: Pipeline %q requires parameter %q, which the catalogue does not supply",
				p.Kind, spec.Name, name))
		}
	}

	return problems
}

// coveredByUpload says whether a parameter receives an artifact URL.
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
				"kind %q: Pipeline %q does not declare workspace %q (expected: %s)",
				p.Kind, spec.Name, workspace.Name, strings.Join(workspaceNames(spec), ", ")))
		}
	}

	for name, optional := range spec.Workspaces {
		if optional || bound[name] {
			continue
		}
		problems = append(problems, fmt.Errorf(
			"kind %q: Pipeline %q requires workspace %q, which the catalogue does not bind",
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
