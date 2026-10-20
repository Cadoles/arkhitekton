package php

import (
	"context"
	"fmt"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capability/interpret"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/parse"
)

// staticReader reads PHPStan and PHP-CS-Fixer.
//
// Violations, not tests, projected onto cases all the same: the question asked is the same, what
// appeared and what disappeared. A violation's identity is its file and its rule, never its line:
// a line moves at every commit.
type staticReader struct{}

func (staticReader) Interpret(_ context.Context, run capability.RunState, records []parse.Record) ([]facts.Set, error) {
	var sets []facts.Set
	if set, found := tool(run, records, "phpstan", "run-phpstan", phpstanSet); found {
		sets = append(sets, set)
	}
	if set, found := tool(run, records, "cs-fixer", "run-cs-fixer", csFixerSet); found {
		sets = append(sets, set)
	}
	return sets, nil
}

// unitReader reads PHPUnit's JUnit report.
type unitReader struct{}

func (unitReader) Interpret(_ context.Context, run capability.RunState, records []parse.Record) ([]facts.Set, error) {
	if set, found := tool(run, records, "phpunit", "run-unit-tests", junitSet("unit")); found {
		return []facts.Set{set}, nil
	}
	return nil, nil
}

// tool reads one tool's record, or says it did not conclude when its task ran and left nothing
// readable.
func tool(
	run capability.RunState, records []parse.Record, name, task string,
	read func(parse.Record) facts.Set,
) (facts.Set, bool) {
	if record, readable := interpret.Readable(records, name); readable {
		set := read(record)
		set.Tool, set.RawObject = name, record.Object
		return set, true
	}
	if summary, ran := interpret.NotConcluded(run, task); ran {
		return facts.Set{
			Shape: facts.ShapeCases, Group: groupOf(name), Tool: name,
			Status: facts.StatusNA, Summary: summary,
		}, true
	}
	return facts.Set{}, false
}

func groupOf(tool string) string {
	if tool == "phpunit" {
		return "unit"
	}
	return "static"
}

func junitSet(group string) func(parse.Record) facts.Set {
	return func(record parse.Record) facts.Set {
		report := record.Payload.(parse.JUnit)
		cases := interpret.JUnitCases(report)
		return facts.Set{
			Shape: facts.ShapeCases, Group: group, Cases: cases,
			DurationMS: report.DurationMS, Summary: interpret.SummarizeTests(cases),
		}
	}
}

func phpstanSet(record parse.Record) facts.Set {
	report := record.Payload.(parse.PHPStan)
	var cases []facts.Case
	// Errors outside a file are failures of the tool, not of the code; they are kept apart under a
	// suite of their own.
	for _, message := range report.Errors {
		cases = append(cases, facts.Case{
			Suite: "phpstan", Name: "erreur d'analyse",
			Status: facts.CaseFailed, Message: parse.FirstLine(message),
		})
	}
	for _, file := range report.Files {
		for _, message := range file.Messages {
			rule := message.Identifier
			if rule == "" {
				// Older versions emit no identifier: the message stands in for the rule.
				rule = parse.FirstLine(message.Message)
			}
			cases = append(cases, facts.Case{
				Suite: file.Path, Name: rule, Status: facts.CaseFailed,
				Message: fmt.Sprintf("line %d: %s", message.Line, parse.FirstLine(message.Message)),
			})
		}
	}
	return violations(cases, "violation(s)")
}

func csFixerSet(record parse.Record) facts.Set {
	report := record.Payload.(parse.CSFixer)
	var cases []facts.Case
	for _, file := range report.Files {
		fixers := file.AppliedFixers
		if len(fixers) == 0 {
			fixers = []string{"style"}
		}
		for _, fixer := range fixers {
			cases = append(cases, facts.Case{
				Suite: file.Name, Name: fixer, Status: facts.CaseFailed,
				Message: "style deviation: " + fixer, Trace: file.Diff,
			})
		}
	}
	return violations(cases, "style deviation(s)")
}

// violations is a static analysis' set. A clean one has no cases and still passes: it ran and
// found nothing, which is not the same as not running.
func violations(cases []facts.Case, noun string) facts.Set {
	set := facts.Set{
		Shape: facts.ShapeCases, Group: "static", Cases: cases,
		Summary: interpret.SummarizeViolations(cases, noun),
	}
	if len(cases) == 0 {
		set.Status = facts.StatusPass
	}
	return set
}
