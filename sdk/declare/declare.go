// Package declare is the schema of a run reading declared rather than written in code.
//
// A pipeline capability may describe how its runs are read: for each tool, which report it reads,
// which rows of that report become test cases and how, how the summary reads, and which exit codes
// mean a result. The dashboard executes the declaration; a plugin brings it as YAML
// (capability.Pipeline.Reading), and this package is what the YAML must say once converted to
// JSON.
//
// Only test cases are declared: findings and measures need aggregates only a dedicated reading
// knows how to state.
package declare

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
)

// APIVersion is the schema version a declaration states.
const APIVersion = "arkhitekton.reading/v1"

// Reading is a declared reading of a pipeline's runs.
type Reading struct {
	APIVersion string `json:"apiVersion"`
	Tools      []Tool `json:"tools"`
}

// Tool is how one tool's report is read.
type Tool struct {
	// Tool names the tool, as the capability's artifact members declare it.
	Tool string `json:"tool"`
	// Task is the pipeline task running the tool: its state tells a tool that ran and left nothing
	// readable from one that never started.
	Task  string `json:"task,omitempty"`
	Group string `json:"group"`
	// Summary is how the set's summary line reads.
	Summary Summary `json:"summary"`
	// Empty is the verdict of a report with no case. Empty, it is derived from the cases: a
	// report that lists nothing measured nothing.
	Empty facts.Status `json:"empty,omitempty"`
	// Duration is read from the report as a whole, in milliseconds.
	Duration *Template `json:"duration,omitempty"`
	// ExitCodes lists the codes that mean the tool produced a result. Any other, and the tool did
	// not conclude, whatever its report says. Absent, the code is not consulted.
	ExitCodes *ExitCodes `json:"exitCodes,omitempty"`
	Cases     []CaseRule `json:"cases"`
}

// ExitCodes are the codes that mean a result.
type ExitCodes struct {
	Results []int32 `json:"results"`
}

// Summary is `tests`, or `{violations: "<noun>"}` for a static analysis.
type Summary struct {
	Tests      bool
	Violations string
}

func (s *Summary) UnmarshalJSON(raw []byte) error {
	var word string
	if err := json.Unmarshal(raw, &word); err == nil {
		if word != "tests" {
			return fmt.Errorf("summary %q: tests, or {violations: noun}", word)
		}
		s.Tests = true
		return nil
	}
	var object struct {
		Violations string `json:"violations"`
	}
	if err := json.Unmarshal(raw, &object); err != nil || strings.TrimSpace(object.Violations) == "" {
		return errors.New("summary: tests, or {violations: noun}")
	}
	s.Violations = object.Violations
	return nil
}

func (s Summary) MarshalJSON() ([]byte, error) {
	if s.Tests {
		return json.Marshal("tests")
	}
	return json.Marshal(map[string]string{"violations": s.Violations})
}

// CaseRule turns each row of a view into a case.
type CaseRule struct {
	// From names a view of the tool's report: `junit.cases`, `phpstan.messages`...
	From    string    `json:"from"`
	Suite   Template  `json:"suite"`
	Name    Template  `json:"name"`
	Status  Status    `json:"status"`
	Message *Template `json:"message,omitempty"`
	Trace   *Template `json:"trace,omitempty"`
	// Duration is the case's, in milliseconds.
	Duration *Template `json:"duration,omitempty"`
}

// Status is a fixed case status, or one read from a field through a table.
type Status struct {
	Fixed facts.CaseStatus
	From  string
	Map   map[string]facts.CaseStatus
}

func (s *Status) UnmarshalJSON(raw []byte) error {
	var fixed facts.CaseStatus
	if err := json.Unmarshal(raw, &fixed); err == nil {
		s.Fixed = fixed
		return nil
	}
	var table struct {
		From string                      `json:"from"`
		Map  map[string]facts.CaseStatus `json:"map"`
	}
	if err := json.Unmarshal(raw, &table); err != nil {
		return errors.New("status: a case status, or {from: field, map: {value: status}}")
	}
	s.From, s.Map = table.From, table.Map
	return nil
}

func (s Status) MarshalJSON() ([]byte, error) {
	if s.From == "" {
		return json.Marshal(s.Fixed)
	}
	return json.Marshal(map[string]any{"from": s.From, "map": s.Map})
}

// Decode reads a declaration from JSON, refusing unknown keys: a misspelt key is a rule that would
// silently not apply.
func Decode(raw []byte) (Reading, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var reading Reading
	if err := decoder.Decode(&reading); err != nil {
		return Reading{}, fmt.Errorf("reading: %w", err)
	}
	return reading, reading.Validate()
}

// Validate checks what can be checked without knowing the views: names, statuses, verdicts. The
// host checks the rest, that each view exists and has the fields the templates read.
func (r Reading) Validate() error {
	var problems []error
	if r.APIVersion != APIVersion {
		problems = append(problems, fmt.Errorf("apiVersion %q, expected %q", r.APIVersion, APIVersion))
	}
	if len(r.Tools) == 0 {
		problems = append(problems, errors.New("no tool"))
	}
	seen := map[string]bool{}
	for i, tool := range r.Tools {
		at := fmt.Sprintf("tools[%d]", i)
		if strings.TrimSpace(tool.Tool) == "" || strings.TrimSpace(tool.Group) == "" {
			problems = append(problems, fmt.Errorf("%s: tool and group are required", at))
		}
		if seen[tool.Tool] {
			problems = append(problems, fmt.Errorf("%s: tool %q read twice", at, tool.Tool))
		}
		seen[tool.Tool] = true
		if !tool.Summary.Tests && tool.Summary.Violations == "" {
			problems = append(problems, fmt.Errorf("%s: no summary", at))
		}
		switch tool.Empty {
		case "", facts.StatusPass, facts.StatusWarn, facts.StatusFail, facts.StatusSkip, facts.StatusNA:
		default:
			problems = append(problems, fmt.Errorf("%s.empty: unknown verdict %q", at, tool.Empty))
		}
		if tool.ExitCodes != nil && (len(tool.ExitCodes.Results) == 0 || tool.Task == "") {
			problems = append(problems, fmt.Errorf("%s.exitCodes: results and the task they are read on", at))
		}
		if len(tool.Cases) == 0 {
			problems = append(problems, fmt.Errorf("%s: no case rule", at))
		}
		for j, rule := range tool.Cases {
			problems = append(problems, rule.problems(fmt.Sprintf("%s.cases[%d]", at, j))...)
		}
	}
	return errors.Join(problems...)
}

func (c CaseRule) problems(at string) []error {
	var problems []error
	if view, part, found := strings.Cut(c.From, "."); !found || view == "" || part == "" {
		problems = append(problems, fmt.Errorf("%s.from: %q, expected parser.view", at, c.From))
	}
	if c.Suite.String() == "" || c.Name.String() == "" {
		problems = append(problems, fmt.Errorf("%s: suite and name are a case's identity", at))
	}
	statuses := []facts.CaseStatus{c.Status.Fixed}
	if c.Status.From != "" {
		if len(c.Status.Map) == 0 {
			problems = append(problems, fmt.Errorf("%s.status: a table maps no value", at))
		}
		statuses = statuses[:0]
		for _, status := range c.Status.Map {
			statuses = append(statuses, status)
		}
	}
	for _, status := range statuses {
		switch status {
		case facts.CasePassed, facts.CaseFailed, facts.CaseSkipped, facts.CaseFlaky:
		default:
			problems = append(problems, fmt.Errorf("%s.status: unknown case status %q", at, status))
		}
	}
	return problems
}

// Fields lists the row fields a case rule reads, its status table included.
func (c CaseRule) Fields() []string {
	fields := append(c.Suite.Fields(), c.Name.Fields()...)
	for _, template := range []*Template{c.Message, c.Trace, c.Duration} {
		if template != nil {
			fields = append(fields, template.Fields()...)
		}
	}
	if c.Status.From != "" {
		fields = append(fields, c.Status.From)
	}
	return fields
}
