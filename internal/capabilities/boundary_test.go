package capabilities

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const module = "forge.cadoles.com/vcarroy/arkhitekton/"

// Plugins depend on the contract and nothing else of the dashboard.
//
// The contract, the sdk module, is what a plugin compiles against. Plugins import it and nothing
// else, so that an internal plugin is written exactly like an external one. And no core package
// imports a plugin: only the assembler, internal/plugins/builtin, lists them, for cmd/arkhitekton and
// for the live tests that observe a real deployment.
func TestThePluginsImportOnlyTheContract(t *testing.T) {
	root := filepath.Join("..", "..", "internal")
	isContract := func(imported string) bool { return strings.HasPrefix(imported, module+"sdk") }
	isPlugin := func(imported string) bool {
		return strings.HasPrefix(imported, module+"internal/plugins/")
	}

	files := token.NewFileSet()
	read := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		parsed, err := parser.ParseFile(files, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		read++

		relative := filepath.ToSlash(strings.TrimPrefix(path, filepath.Join("..", "..")+string(filepath.Separator)))
		assembler := strings.HasPrefix(relative, "internal/plugins/builtin/")
		inPlugin := strings.HasPrefix(relative, "internal/plugins/") && !assembler
		live := strings.HasSuffix(relative, "_live_test.go")

		for _, spec := range parsed.Imports {
			imported, _ := strconv.Unquote(spec.Path.Value)
			if !strings.HasPrefix(imported, module) {
				continue
			}
			switch {
			case assembler:
				if !isContract(imported) && !isPlugin(imported) && imported != module+"internal/capabilities" {
					t.Errorf("%s imports %s: the assembler imports only plugins and their registry", path, imported)
				}
			case inPlugin && !isContract(imported):
				t.Errorf("%s imports %s: plugins import only the contract", path, imported)
			case !inPlugin && isPlugin(imported) && !(live && imported == module+"internal/plugins/builtin"):
				t.Errorf("%s imports %s: only the assembler lists the plugins", path, imported)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reading the sources: %v", err)
	}
	// A wrong starting path would read nothing, and the test would pass having checked nothing.
	if read < 100 {
		t.Fatalf("only %d files read under %s", read, root)
	}
}
