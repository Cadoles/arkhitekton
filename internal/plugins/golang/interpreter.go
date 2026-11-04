package golang

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/interpret"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// The pipelines' tasks whose exit code says whether what they left is whole.
const (
	buildTask     = "build-binaries"
	vulncheckTask = "run-govulncheck"
)

// unfinished is the set of a tool that stopped before its end, killed for memory for instance:
// what it left is partial, and reading it would pass a crash off as a result.
func unfinished(run capability.RunState, task string, set facts.Set) (facts.Set, bool) {
	for _, state := range run.Tasks {
		if state.Name != task {
			continue
		}
		for _, step := range state.Steps {
			if step.ExitCode != nil && *step.ExitCode != 0 {
				set.Status = facts.StatusNA
				set.Summary, _ = interpret.NotConcluded(run, task)
				return set, true
			}
		}
	}
	return set, false
}

// binaries reads a build's files: the archives and their checksums, kept with the release.
type binaries struct{}

func (binaries) Interpret(_ context.Context, run capability.RunState, records []parse.Record) ([]facts.Set, error) {
	set := facts.Set{Shape: facts.ShapeAttachments, Group: "build", Tool: "go-build", Status: facts.StatusPass}
	// A failed build offers none of its archives: some platforms may have built, not the release.
	if failed, stopped := unfinished(run, buildTask, set); stopped {
		return []facts.Set{failed}, nil
	}
	archives := 0
	for _, record := range records {
		attached, ok := record.Payload.(parse.Attached)
		if record.Err != nil || !ok {
			return nil, fmt.Errorf("build file %s unreadable: %v", record.Member, record.Err)
		}
		name := path.Base(record.Member)
		if strings.HasSuffix(name, ".tar.gz") {
			archives++
		}
		set.Attachments = append(set.Attachments, facts.Attachment{
			Name: name, MediaType: attached.MediaType, Object: attached.Object, Size: attached.Size,
		})
	}
	if archives == 0 {
		return nil, fmt.Errorf("the build published no binary")
	}
	sort.Slice(set.Attachments, func(i, j int) bool { return set.Attachments[i].Name < set.Attachments[j].Name })
	set.Summary = fmt.Sprintf("%d binaire(s)", archives)
	return []facts.Set{set}, nil
}

/*
 * govulncheck's JSON stream.
 *
 * A finding is at one of three levels, which its trace says: a symbol the code calls (its first
 * frame names a function), a package it imports, a module it requires. Only the first exposes the
 * code: it is a high finding, the score's concern. The other two are kept, low and informative, so
 * that the list says what to upgrade without the release being held for code it does not run.
 */

type vulnMessage struct {
	OSV *struct {
		ID       string   `json:"id"`
		Summary  string   `json:"summary"`
		Details  string   `json:"details"`
		Aliases  []string `json:"aliases"`
		Database struct {
			URL string `json:"url"`
		} `json:"database_specific"`
	} `json:"osv"`
	Finding *struct {
		OSV          string `json:"osv"`
		FixedVersion string `json:"fixed_version"`
		Trace        []struct {
			Module   string `json:"module"`
			Version  string `json:"version"`
			Package  string `json:"package"`
			Function string `json:"function"`
			Receiver string `json:"receiver"`
			Position *struct {
				Filename string `json:"filename"`
				Line     int    `json:"line"`
			} `json:"position"`
		} `json:"trace"`
	} `json:"finding"`
}

// vulnerabilities reads govulncheck's findings into a security audit.
type vulnerabilities struct{}

func (vulnerabilities) Interpret(_ context.Context, run capability.RunState, records []parse.Record) ([]facts.Set, error) {
	audit := facts.Set{Shape: facts.ShapeFindings, Group: "security", Tool: "govulncheck"}
	if failed, stopped := unfinished(run, vulncheckTask, audit); stopped {
		return []facts.Set{failed}, nil
	}
	for _, record := range records {
		raw, ok := record.Payload.(parse.Bytes)
		if record.Err != nil || !ok {
			return nil, fmt.Errorf("govulncheck report %s unreadable: %v", record.Member, record.Err)
		}
		findings, err := readVulnerabilities(raw)
		if err != nil {
			return nil, err
		}
		set := audit
		set.RawObject, set.Findings, set.Status = record.Object, findings, facts.StatusPass
		called := findings.BySeverity[facts.SeverityHigh]
		if called > 0 {
			set.Status = facts.StatusFail
		}
		set.Summary = fmt.Sprintf("%d vulnérabilité(s) appelée(s), %d au total", called, findings.Total)
		return []facts.Set{set}, nil
	}
	return nil, fmt.Errorf("no govulncheck report")
}

var levelSeverity = map[int]facts.Severity{3: facts.SeverityHigh, 2: facts.SeverityLow, 1: facts.SeverityInfo}

func readVulnerabilities(raw []byte) (*facts.Findings, error) {
	type entry struct {
		id, title, description, url string
		aliases                     []string
	}
	entries := map[string]entry{}
	type found struct {
		level            int
		module, version  string
		pkg, fixed, call string
	}
	worst := map[string]found{}
	var order []string
	messages := 0

	decoder := json.NewDecoder(bufio.NewReader(bytes.NewReader(raw)))
	for {
		var message vulnMessage
		if err := decoder.Decode(&message); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("govulncheck report: %w", err)
		}
		messages++
		if message.OSV != nil {
			entries[message.OSV.ID] = entry{
				id: message.OSV.ID, title: message.OSV.Summary, description: message.OSV.Details,
				url: message.OSV.Database.URL, aliases: message.OSV.Aliases,
			}
		}
		if message.Finding == nil || len(message.Finding.Trace) == 0 {
			continue
		}
		frame := message.Finding.Trace[0]
		level := 1
		switch {
		case frame.Function != "":
			level = 3
		case frame.Package != "":
			level = 2
		}
		current := found{
			level: level, module: frame.Module, version: frame.Version, pkg: frame.Package,
			fixed: message.Finding.FixedVersion,
		}
		if level == 3 {
			last := message.Finding.Trace[len(message.Finding.Trace)-1]
			current.call = frame.Function
			if last.Position != nil {
				current.call = fmt.Sprintf("%s:%d", last.Position.Filename, last.Position.Line)
			}
		}
		previous, seen := worst[message.Finding.OSV]
		if !seen {
			order = append(order, message.Finding.OSV)
		}
		if !seen || current.level > previous.level {
			worst[message.Finding.OSV] = current
		}
	}
	if messages == 0 {
		return nil, fmt.Errorf("govulncheck report: empty")
	}

	findings := &facts.Findings{BySeverity: map[facts.Severity]int{}}
	for _, id := range order {
		hit := worst[id]
		described := entries[id]
		severity := levelSeverity[hit.level]
		title := described.title
		if title == "" {
			title = id
		}
		rule := id
		if len(described.aliases) > 0 {
			rule = id + " (" + strings.Join(described.aliases, ", ") + ")"
		}
		location := hit.pkg
		if hit.call != "" {
			location = hit.call
		}
		findings.Items = append(findings.Items, facts.Finding{
			Fingerprint: id + ":" + hit.module, TargetKind: "module", TargetName: hit.module,
			Tool: "govulncheck", Category: "vulnerability", RuleID: rule, Severity: severity,
			Title: title, Description: described.description, Location: location,
			PackageName: hit.module, PackageInstalled: hit.version, PackageFixed: hit.fixed,
			Fixable: hit.fixed != "", Status: "open",
		})
		findings.BySeverity[severity]++
		findings.Total++
	}
	return findings, nil
}
