package declarative

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"sigs.k8s.io/yaml"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/declare"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/interpret"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

/*
 * Executing a declared reading.
 *
 * The same reading the built-in interpreters do, step for step: a tool's readable report becomes a
 * set of cases; a task that ran and left nothing readable did not conclude; a task that never
 * started says nothing. What the declaration chooses is only which rows become cases, and how
 * their fields are named.
 */

// Compile reads a declaration and checks it against the views and the pipeline it reads: every
// view must exist and read what the pipeline's artifacts parse for that tool, every field a
// template reads must be one the view offers, every status table must cover its field's values.
// What is refused here is refused at start-up rather than discovered on a run.
func Compile(source []byte, spec capability.PipelineSpec) (capability.Interpreter, error) {
	converted, err := yaml.YAMLToJSON(source)
	if err != nil {
		return nil, fmt.Errorf("reading: %w", err)
	}
	reading, err := declare.Decode(converted)
	if err != nil {
		return nil, err
	}

	parsersOf := map[string][]parse.ID{}
	for _, artifact := range spec.Artifacts {
		for _, member := range artifact.Members {
			if member.Parser != parse.Attach && !slices.Contains(parsersOf[member.Tool], member.Parser) {
				parsersOf[member.Tool] = append(parsersOf[member.Tool], member.Parser)
			}
		}
	}

	var problems []error
	compiled := interpreter{}
	for i, tool := range reading.Tools {
		at := fmt.Sprintf("tools[%d]", i)
		parsed := parsersOf[tool.Tool]
		if len(parsed) == 0 {
			problems = append(problems, fmt.Errorf("%s: no artifact of the pipeline is read for tool %q", at, tool.Tool))
			continue
		}
		entry := compiledTool{Tool: tool}
		for j, rule := range tool.Cases {
			ruleAt := fmt.Sprintf("%s.cases[%d]", at, j)
			view, found := Lookup(rule.From)
			if !found {
				problems = append(problems, fmt.Errorf("%s.from: no view %q", ruleAt, rule.From))
				continue
			}
			if !slices.Contains(parsed, view.Parser) {
				problems = append(problems, fmt.Errorf("%s.from: view %q reads %s, tool %q is parsed with %v",
					ruleAt, rule.From, view.Parser, tool.Tool, parsed))
			}
			problems = append(problems, fieldProblems(ruleAt, rule.Fields(), view.Fields)...)
			if values, enumerated := view.Enums[rule.Status.From]; enumerated {
				for _, value := range values {
					if _, mapped := rule.Status.Map[value]; !mapped {
						problems = append(problems, fmt.Errorf("%s.status: %s %q is not mapped", ruleAt, rule.Status.From, value))
					}
				}
			}
			entry.rules = append(entry.rules, compiledRule{CaseRule: rule, view: view})
		}
		if tool.Duration != nil {
			for _, parser := range parsed {
				problems = append(problems, fieldProblems(at+".duration", tool.Duration.Fields(), reportFields(parser))...)
			}
		}
		compiled.tools = append(compiled.tools, entry)
	}
	if err := errors.Join(problems...); err != nil {
		return nil, err
	}
	return compiled, nil
}

func fieldProblems(at string, used, offered []string) []error {
	var problems []error
	for _, field := range used {
		if !slices.Contains(offered, field) {
			problems = append(problems, fmt.Errorf("%s: field %q is not offered (offered: %v)", at, field, offered))
		}
	}
	return problems
}

type interpreter struct {
	tools []compiledTool
}

type compiledTool struct {
	declare.Tool
	rules []compiledRule
}

type compiledRule struct {
	declare.CaseRule
	view View
}

// Interpret reads a finished run as the declaration says.
func (r interpreter) Interpret(_ context.Context, run capability.RunState, records []parse.Record) ([]facts.Set, error) {
	var sets []facts.Set
	for _, tool := range r.tools {
		if set, found := tool.read(run, records); found {
			sets = append(sets, set)
		}
	}
	return sets, nil
}

func (t compiledTool) read(run capability.RunState, records []parse.Record) (facts.Set, bool) {
	record, readable := interpret.Readable(records, t.Tool.Tool)
	set := facts.Set{Shape: facts.ShapeCases, Group: t.Group, Tool: t.Tool.Tool}
	if readable {
		set.RawObject = record.Object
	}

	// A code outside the results is a failure of the tool, whatever its report says, and whether
	// it left one or not.
	if t.ExitCodes != nil {
		if code, ran := exitCode(run, t.Task); ran && !slices.Contains(t.ExitCodes.Results, code) {
			set.Status = facts.StatusNA
			set.Summary = fmt.Sprintf("the tool did not conclude (code %d)", code)
			return set, true
		}
	}

	if !readable {
		if t.Task == "" {
			return facts.Set{}, false
		}
		summary, ran := interpret.NotConcluded(run, t.Task)
		if !ran {
			return facts.Set{}, false
		}
		set.Status, set.Summary = facts.StatusNA, summary
		return set, true
	}

	for _, rule := range t.rules {
		if rule.view.Parser != record.Parser {
			continue
		}
		for _, row := range rule.view.Rows(record.Payload) {
			set.Cases = append(set.Cases, rule.build(row))
		}
	}
	if t.Duration != nil {
		set.DurationMS, _ = strconv.ParseInt(t.Duration.Render(reportRow(record.Parser, record.Payload)), 10, 64)
	}
	if t.Summary.Tests {
		set.Summary = interpret.SummarizeTests(set.Cases)
	} else {
		set.Summary = interpret.SummarizeViolations(set.Cases, t.Summary.Violations)
	}
	if len(set.Cases) == 0 && t.Empty != "" {
		set.Status = t.Empty
	}
	return set, true
}

func (r compiledRule) build(row Row) facts.Case {
	built := facts.Case{Suite: r.Suite.Render(row), Name: r.Name.Render(row), Status: r.Status.Fixed}
	if r.Status.From != "" {
		built.Status = r.Status.Map[row[r.Status.From]]
	}
	if r.Message != nil {
		built.Message = r.Message.Render(row)
	}
	if r.Trace != nil {
		built.Trace = r.Trace.Render(row)
	}
	if r.CaseRule.Duration != nil {
		built.DurationMS, _ = strconv.Atoi(r.CaseRule.Duration.Render(row))
	}
	return built
}

// exitCode is the first non-zero exit code of a task's steps, or zero when they all succeeded.
func exitCode(run capability.RunState, task string) (int32, bool) {
	for _, state := range run.Tasks {
		if state.Name != task {
			continue
		}
		var found bool
		for _, step := range state.Steps {
			if step.ExitCode == nil {
				continue
			}
			found = true
			if *step.ExitCode != 0 {
				return *step.ExitCode, true
			}
		}
		return 0, found
	}
	return 0, false
}
