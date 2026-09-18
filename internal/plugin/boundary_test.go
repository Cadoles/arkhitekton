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

// Le noyau n'importe pas le plugin MSE.
//
// C'est ce qui garantit qu'une instance sans lui reste cohérente : un paquet du
// noyau qui l'importerait ne compilerait pas sans lui, et le module éteint ne
// serait qu'un interrupteur d'affichage. Seul `cmd/arkhitekton` l'assemble
// (doc 16 §16.6). Les tests comptent aussi : un test du noyau qui l'importerait
// ferait du plugin une dépendance de sa vérification.
func TestLeNoyauNImportePasLePluginMSE(t *testing.T) {
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
				t.Errorf("%s importe %s — seul cmd/arkhitekton assemble le plugin", path, imported)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("lecture des sources: %v", err)
	}
	// Un chemin de départ faux ne lirait rien, et le test passerait sans rien
	// vérifier.
	if read < 100 {
		t.Fatalf("seulement %d fichiers lus sous %s", read, root)
	}
}
