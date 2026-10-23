package mse

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The plugin is written against what a distribution lends it, and nothing else of the dashboard:
// that is what lets it build outside this repository.
func TestThePluginImportsOnlyTheHost(t *testing.T) {
	const module = "forge.cadoles.com/vcarroy/arkhitekton/"
	allowed := []string{module + "host", module + "sdk", module + "internal/mse"}

	files := token.NewFileSet()
	read := 0
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		parsed, err := parser.ParseFile(files, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		read++
		for _, spec := range parsed.Imports {
			imported, _ := strconv.Unquote(spec.Path.Value)
			if !strings.HasPrefix(imported, module) {
				continue
			}
			permitted := false
			for _, prefix := range allowed {
				permitted = permitted || imported == prefix || strings.HasPrefix(imported, prefix+"/")
			}
			if !permitted {
				t.Errorf("%s imports %s", path, imported)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if read < 20 {
		t.Fatalf("only %d files read", read)
	}
}
