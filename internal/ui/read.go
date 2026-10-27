package ui

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	schema "forge.cadoles.com/vcarroy/arkhitekton/sdk/ui"
)

// Read reads the resources of a YAML file, one per document.
func Read(raw []byte) ([]schema.Resource, error) {
	reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(raw)))
	var resources []schema.Resource
	for index := 0; ; index++ {
		document, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return resources, nil
		}
		if err != nil {
			return nil, err
		}
		converted, err := yaml.YAMLToJSON(document)
		if err != nil {
			return nil, fmt.Errorf("document %d: %w", index, err)
		}
		if string(bytes.TrimSpace(converted)) == "null" {
			continue
		}
		resource, err := schema.Decode(converted)
		if err != nil {
			return nil, fmt.Errorf("document %d: %w", index, err)
		}
		resources = append(resources, resource)
	}
}

// ReadFiles reads the resources of the YAML files matched in a file system, in the order of their
// names.
func ReadFiles(files fs.FS, patterns ...string) ([]schema.Resource, error) {
	var names []string
	for _, pattern := range patterns {
		matched, err := fs.Glob(files, pattern)
		if err != nil {
			return nil, err
		}
		names = append(names, matched...)
	}
	slices.Sort(names)
	names = slices.Compact(names)
	var resources []schema.Resource
	for _, name := range names {
		raw, err := fs.ReadFile(files, name)
		if err != nil {
			return nil, err
		}
		read, err := Read(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path.Clean(name), err)
		}
		resources = append(resources, read...)
	}
	return resources, nil
}
