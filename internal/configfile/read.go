package configfile

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// document is a resource's envelope.
type document struct {
	APIVersion string          `json:"apiVersion"`
	Kind       string          `json:"kind"`
	Metadata   *metadata       `json:"metadata,omitempty"`
	Spec       json.RawMessage `json:"spec"`
}

type metadata struct {
	Name string `json:"name,omitempty"`
}

// Read reads every resource of a YAML stream, one per document, then checks the whole set.
// source names the stream in error messages.
func Read(raw []byte, source string) (Set, error) {
	var set Set
	if err := set.add(raw, source); err != nil {
		return Set{}, err
	}
	return set, set.Validate()
}

// ReadPath reads a file, a directory (its `.yaml` and `.yml` files, recursively, in the order of
// their paths) or `-` for the standard input.
func ReadPath(path string, stdin io.Reader) (Set, error) {
	if path == "-" {
		raw, err := io.ReadAll(stdin)
		if err != nil {
			return Set{}, fmt.Errorf("reading the standard input: %w", err)
		}
		return Read(raw, "stdin")
	}

	info, err := os.Stat(path)
	if err != nil {
		return Set{}, err
	}
	if !info.IsDir() {
		raw, err := os.ReadFile(path)
		if err != nil {
			return Set{}, err
		}
		return Read(raw, path)
	}

	var files []string
	err = filepath.WalkDir(path, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && (strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")) {
			files = append(files, name)
		}
		return nil
	})
	if err != nil {
		return Set{}, err
	}
	slices.Sort(files)

	var set Set
	for _, name := range files {
		raw, err := os.ReadFile(name)
		if err != nil {
			return Set{}, err
		}
		relative, _ := filepath.Rel(path, name)
		if err := set.add(raw, relative); err != nil {
			return Set{}, err
		}
	}
	return set, set.Validate()
}

func (s *Set) add(raw []byte, source string) error {
	reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(raw)))
	for index := 0; ; index++ {
		chunk, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", source, err)
		}
		converted, err := yaml.YAMLToJSON(chunk)
		if err != nil {
			return fmt.Errorf("%s, document %d: %w", source, index, err)
		}
		if trimmed := string(bytes.TrimSpace(converted)); trimmed == "null" || trimmed == "" {
			continue
		}
		if err := s.decode(converted); err != nil {
			return fmt.Errorf("%s, document %d: %w", source, index, err)
		}
	}
}

func (s *Set) decode(raw []byte) error {
	var envelope document
	if err := strict(raw, &envelope); err != nil {
		return err
	}
	if envelope.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion %q: expected %s", envelope.APIVersion, APIVersion)
	}

	switch envelope.Kind {
	case KindInstance:
		if s.Instance != nil {
			return errors.New("a second Instance: the instance is declared once")
		}
		var instance Instance
		if err := strict(envelope.Spec, &instance); err != nil {
			return fmt.Errorf("instance: spec: %w", err)
		}
		s.Instance = &instance
	case KindProject:
		var name string
		if envelope.Metadata != nil {
			name = strings.TrimSpace(envelope.Metadata.Name)
		}
		var project Project
		if err := strict(envelope.Spec, &project); err != nil {
			return fmt.Errorf("project %q: spec: %w", name, err)
		}
		project.Name = name
		s.Projects = append(s.Projects, project)
	default:
		return fmt.Errorf("kind %q: expected %s or %s", envelope.Kind, KindInstance, KindProject)
	}
	return nil
}

// strict decodes JSON refusing unknown fields: a misspelled key in a hand-written file must be an
// error, not a setting silently ignored.
func strict(raw []byte, into any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(into)
}
