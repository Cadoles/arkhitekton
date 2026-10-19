package capability

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

// The contract packages and the plugins depend on nothing else of the dashboard.
//
// The contract (capability, facts, parse) is what a plugin compiles against: if it imported the
// store or the worker, so would every plugin, and moving it out of internal/ later would drag the
// dashboard along. Plugins import the contract and nothing else, so that an internal plugin is
// written exactly like an external one. And no core package imports a plugin: only the assembler,
// internal/plugins/builtin, lists them, for cmd/arkhitekton and for the live tests that observe a
// real deployment.
func TestTheContractAndThePluginsStandAlone(t *testing.T) {
	root := filepath.Join("..", "..", "internal")
	contract := []string{"internal/capability", "internal/facts", "internal/parse"}
	isContract := func(imported string) bool {
		for _, prefix := range contract {
			if imported == module+prefix || strings.HasPrefix(imported, module+prefix+"/") {
				return true
			}
		}
		return false
	}
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
		inContract := isContract(module + filepath.ToSlash(filepath.Dir(relative)))
		inPlugin := strings.HasPrefix(relative, "internal/plugins/")
		assembler := strings.HasPrefix(relative, "internal/plugins/builtin/")
		live := strings.HasSuffix(relative, "_live_test.go")

		for _, spec := range parsed.Imports {
			imported, _ := strconv.Unquote(spec.Path.Value)
			if !strings.HasPrefix(imported, module) {
				continue
			}
			switch {
			case assembler:
				if !isContract(imported) && !isPlugin(imported) {
					t.Errorf("%s imports %s: the assembler imports only plugins", path, imported)
				}
			case (inContract || inPlugin) && !isContract(imported):
				t.Errorf("%s imports %s: the contract and the plugins import only the contract", path, imported)
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
