package configfile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// InstanceFile and ProjectsDir lay out an exported directory.
const (
	InstanceFile = "instance.yaml"
	ProjectsDir  = "projects"
)

// Encode writes a set as YAML documents: the instance first, then the projects by name.
//
// Deterministic, so that two exports of the same configuration are byte for byte identical and a
// change reads as a small diff: keys sorted, lists in a stable order, nothing time-dependent.
func Encode(set Set) ([]byte, error) {
	files, err := Files(set)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.SortFunc(names, func(a, b string) int {
		// The instance before the projects, whatever the names sort to.
		switch {
		case a == InstanceFile:
			return -1
		case b == InstanceFile:
			return 1
		}
		return strings.Compare(a, b)
	})
	var out bytes.Buffer
	for index, name := range names {
		if index > 0 {
			out.WriteString("---\n")
		}
		out.Write(files[name])
	}
	return out.Bytes(), nil
}

// Files lays a set out as files, by path relative to an export directory.
func Files(set Set) (map[string][]byte, error) {
	files := map[string][]byte{}
	if set.Instance != nil {
		raw, err := resource(KindInstance, "", set.Instance)
		if err != nil {
			return nil, err
		}
		files[InstanceFile] = raw
	}
	for _, project := range set.Projects {
		sorted := project
		slices.SortFunc(sorted.Reactions, func(a, b Reaction) int { return strings.Compare(nameKey(a.Name), nameKey(b.Name)) })
		slices.SortFunc(sorted.Schedules, func(a, b Schedule) int { return strings.Compare(nameKey(a.Name), nameKey(b.Name)) })
		raw, err := resource(KindProject, project.Name, sorted)
		if err != nil {
			return nil, err
		}
		files[filepath.Join(ProjectsDir, project.Name+".yaml")] = raw
	}
	return files, nil
}

// WriteDir writes a set's files under a directory, replacing those of the same name. A project file
// the set no longer has is removed: applying the directory again would bring the project back.
func WriteDir(dir string, set Set) error {
	files, err := Files(set)
	if err != nil {
		return err
	}
	stale, _ := filepath.Glob(filepath.Join(dir, ProjectsDir, "*.yaml"))
	for _, path := range stale {
		relative, _ := filepath.Rel(dir, path)
		if _, kept := files[relative]; !kept {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	for name, raw := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func resource(kind, name string, spec any) ([]byte, error) {
	body, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", kind, name, err)
	}
	envelope := document{APIVersion: APIVersion, Kind: kind, Spec: body}
	if name != "" {
		envelope.Metadata = &metadata{Name: name}
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	// Through a node rather than sigs.k8s.io/yaml, which sorts every key: the fields keep the
	// schema's order (a project's repository first, a reaction's name first), maps stay sorted by
	// encoding/json.
	var node yaml.Node
	if err := yaml.Unmarshal(raw, &node); err != nil {
		return nil, err
	}
	plainStyle(&node)
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&node); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// plainStyle drops the JSON flow style the node was read with, so that the output is block YAML
// and strings are quoted only where YAML needs it.
func plainStyle(node *yaml.Node) {
	node.Style = 0
	for _, child := range node.Content {
		plainStyle(child)
	}
}
