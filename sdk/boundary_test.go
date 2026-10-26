package sdk

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The contract imports only the standard library and itself: whatever it imports, every plugin
// imports too.
func TestTheContractImportsOnlyTheStandardLibrary(t *testing.T) {
	const module = "forge.cadoles.com/vcarroy/arkhitekton/sdk"

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
			first, _, _ := strings.Cut(imported, "/")
			standard := !strings.Contains(first, ".")
			if !standard && imported != module && !strings.HasPrefix(imported, module+"/") {
				t.Errorf("%s imports %s", path, imported)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reading the sources: %v", err)
	}
	if read < 10 {
		t.Fatalf("only %d files read", read)
	}
}
