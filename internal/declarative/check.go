package declarative

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/parsers"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

/*
 * Checking a reading against recorded runs, before shipping it: a declaration, or a plugin's
 * program (CheckWith).
 *
 * A case is a directory:
 *
 *   state.json     the run's state, a capability.RunState (empty object: nothing started)
 *   records.json   the reports, each [{"tool": ..., "parser": ..., "file": ...}], file relative
 *                  to the case; without a file, the tool is declared and left no report
 *   expected.json  the facts the reading must produce, as the dashboard stores them
 *
 * The reports are parsed by the dashboard's own parsers, the reading is checked as it is at
 * start-up, and its facts compared with the expected ones.
 */

// CaseRecord is one report of a case.
type CaseRecord struct {
	Tool   string   `json:"tool"`
	Parser parse.ID `json:"parser"`
	File   string   `json:"file,omitempty"`
}

// Check reads a case with a declared reading, and returns the facts produced and the facts
// expected, both as indented JSON. expected is nil when the case records none yet.
func Check(reading []byte, dir string) (got, expected []byte, err error) {
	return check(dir, func(spec capability.PipelineSpec) (capability.Interpreter, func(), error) {
		interpreter, err := Compile(reading, spec)
		return interpreter, func() {}, err
	})
}

// CheckWith reads a case with an interpreter already built: a plugin's program.
func CheckWith(interpreter capability.Interpreter, dir string) (got, expected []byte, err error) {
	return check(dir, func(capability.PipelineSpec) (capability.Interpreter, func(), error) {
		return interpreter, func() {}, nil
	})
}

func check(dir string, build func(capability.PipelineSpec) (capability.Interpreter, func(), error)) (got, expected []byte, err error) {
	var state capability.RunState
	if err := readJSON(filepath.Join(dir, "state.json"), &state); err != nil {
		return nil, nil, err
	}
	var listed []CaseRecord
	if err := readJSON(filepath.Join(dir, "records.json"), &listed); err != nil {
		return nil, nil, err
	}

	var spec capability.PipelineSpec
	var records []parse.Record
	for _, entry := range listed {
		spec.Artifacts = append(spec.Artifacts, capability.Artifact{Name: entry.Tool, Members: []capability.Member{
			{Match: entry.File, Parser: entry.Parser, Tool: entry.Tool},
		}})
		if entry.File == "" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.File))
		if err != nil {
			return nil, nil, fmt.Errorf("case %s: %w", dir, err)
		}
		parsing, known := parsers.Lookup(entry.Parser)
		if !known {
			return nil, nil, fmt.Errorf("case %s: no parser %q", dir, entry.Parser)
		}
		record := parse.Record{Artifact: "case", Object: "raw/case/" + entry.File, Parser: entry.Parser, Tool: entry.Tool}
		record.Payload, record.Err = parsing(raw)
		records = append(records, record)
	}

	interpreter, release, err := build(spec)
	if err != nil {
		return nil, nil, err
	}
	defer release()
	sets, err := interpreter.Interpret(context.Background(), state, records)
	if err != nil {
		return nil, nil, err
	}
	for _, set := range sets {
		if err := set.Validate(); err != nil {
			return nil, nil, fmt.Errorf("case %s: the reading produced facts the dashboard refuses: %w", dir, err)
		}
	}
	got, err = json.MarshalIndent(sets, "", "  ")
	if err != nil {
		return nil, nil, err
	}

	expected, err = os.ReadFile(filepath.Join(dir, "expected.json"))
	if os.IsNotExist(err) {
		return got, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	normalised, err := normalise(expected)
	if err != nil {
		return nil, nil, fmt.Errorf("case %s: expected.json: %w", dir, err)
	}
	return got, normalised, nil
}

// normalise re-indents JSON the way Check writes it, so that two equal fact lists compare equal
// whatever their layout.
func normalise(raw []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(out.Bytes()), nil
}

// Same says whether the facts produced are the facts expected.
func Same(got, expected []byte) bool {
	normalised, err := normalise(got)
	return err == nil && bytes.Equal(normalised, expected)
}

func readJSON(path string, into any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
