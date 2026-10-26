package capability

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// LoadManifests reads the Tekton definitions a plugin ships in a directory of its embedding.
//
// The YAML is kept as it is: the dashboard installs what the plugin declares, and a manifest
// rewritten on the fly would be a definition nobody has reviewed.
func LoadManifests(fsys fs.FS, dir string) ([]Manifest, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var manifests []Manifest
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		raw, err := fs.ReadFile(fsys, path.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		name, kind, err := identify(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		manifests = append(manifests, Manifest{Kind: kind, Name: name, YAML: raw})
	}
	return manifests, nil
}

// identify reads a manifest's name and kind.
//
// A line-by-line read rather than a complete YAML decode: only two leading fields are sought, and
// decoding the whole object would require knowing Tekton's schema for nothing.
func identify(manifest []byte) (name, kind string, err error) {
	for line := range strings.SplitSeq(string(manifest), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case kind == "" && strings.HasPrefix(trimmed, "kind:"):
			kind = strings.TrimSpace(strings.TrimPrefix(trimmed, "kind:"))
		case name == "" && strings.HasPrefix(line, "  name:"):
			// Indented by two spaces: it is `metadata.name`, and not the `name` of a parameter or a
			// step, which live deeper.
			name = strings.TrimSpace(strings.TrimPrefix(trimmed, "name:"))
		}
		if name != "" && kind != "" {
			break
		}
	}
	if name == "" || kind == "" {
		return "", "", fmt.Errorf("manifest with no name or no kind (read: %q/%q)", kind, name)
	}
	return name, kind, nil
}
