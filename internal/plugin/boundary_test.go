package plugin

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The core does not import the MSE plugin.
//
// This is what guarantees that an instance without it stays coherent: a core
// package importing it would not compile without it, and switching the module off
// would be a display toggle and nothing more. Only the app package assembles it.
// Tests count too: a core test importing it would make the plugin a dependency of
// the core's own verification.
func TestTheCoreDoesNotImportTheMSEPlugin(t *testing.T) {
	const pluginPath = "forge.cadoles.com/vcarroy/arkhitekton/internal/mse"
	root := filepath.Join("..", "..", "internal")
	files := token.NewFileSet()
	read := 0

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == filepath.Join(root, "mse") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		parsed, err := parser.ParseFile(files, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		read++
		for _, spec := range parsed.Imports {
			imported, _ := strconv.Unquote(spec.Path.Value)
			if imported == pluginPath || strings.HasPrefix(imported, pluginPath+"/") {
				t.Errorf("%s imports %s — only the app package assembles the plugin", path, imported)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reading the sources: %v", err)
	}
	// A wrong starting path would read nothing, and the test would pass having
	// checked nothing.
	if read < 100 {
		t.Fatalf("only %d files read under %s", read, root)
	}
}
