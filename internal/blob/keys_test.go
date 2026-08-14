package blob

import (
	"sort"
	"strings"
	"testing"
)

func TestKeysFollowDocumentedLayout(t *testing.T) {
	var k Keys

	cases := []struct {
		name     string
		got      func() (string, error)
		expected string
	}{
		{"artefact brut", func() (string, error) {
			return k.RawArtifact("php-static-tests-run-abc", "junit-unit.xml")
		}, "raw/php-static-tests-run-abc/junit-unit.xml"},

		{"descripteur meta", func() (string, error) {
			return k.RawMeta("security-audit-run-xyz", "trivy-fs")
		}, "raw/security-audit-run-xyz/meta/trivy-fs.json"},

		{"log d'étape", func() (string, error) {
			return k.TaskLog("php-integration-test-run-1", "run-integration-tests", "step-0")
		}, "logs/php-integration-test-run-1/run-integration-tests/step-0.log"},

		{"livrable de release", func() (string, error) {
			return k.ReleaseArtifact("1.8.0-rc3", "delivery-note.pdf")
		}, "artifacts/releases/1.8.0-rc3/delivery-note.pdf"},

		{"préfixe de run", func() (string, error) {
			return k.RunPrefix("build-image-run-42")
		}, "raw/build-image-run-42/"},
	}

	for _, c := range cases {
		got, err := c.got()
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.expected {
			t.Errorf("%s = %q, attendu %q", c.name, got, c.expected)
		}
	}
}

// Les tags de release sont saisis par un utilisateur. S3 n'a pas de notion de
// répertoire pour s'opposer à « ../ » : la clé est une chaîne libre, et la
// validation est la seule barrière.
func TestKeysRejectPathTraversal(t *testing.T) {
	var k Keys

	hostile := []string{
		"..",
		".",
		"../../etc",
		"a/b",
		"/absolu",
		"",
		"..%2f..%2fpasswd",
		"tag avec espaces",
		"-commence-par-tiret",
		"tag\nmultiligne",
	}

	for _, segment := range hostile {
		if key, err := k.ReleaseArtifact(segment, "note.pdf"); err == nil {
			t.Errorf("segment hostile %q accepté, clé produite: %q", segment, key)
		}
		if key, err := k.RawArtifact("run-ok", segment); err == nil {
			t.Errorf("nom d'artefact hostile %q accepté, clé produite: %q", segment, key)
		}
	}
}

// Une clé produite ne doit jamais sortir de son préfixe, quelle que soit
// l'entrée acceptée.
func TestValidKeysStayWithinTheirPrefix(t *testing.T) {
	var k Keys

	valid := []string{"1.8.0", "1.8.0-rc3", "run-abc-123", "a_b.c-d", "X"}

	for _, segment := range valid {
		key, err := k.ReleaseArtifact(segment, "note.pdf")
		if err != nil {
			t.Errorf("segment légitime %q refusé: %v", segment, err)
			continue
		}
		if !strings.HasPrefix(key, "artifacts/releases/") {
			t.Errorf("la clé %q est sortie de son préfixe", key)
		}
		if strings.Contains(key, "..") {
			t.Errorf("la clé %q contient une remontée", key)
		}
	}
}

func TestUnsafeSegmentErrorNamesTheOffendingValue(t *testing.T) {
	var k Keys

	_, err := k.ReleaseArtifact("../evasion", "note.pdf")
	if err == nil {
		t.Fatal("segment hostile accepté")
	}
	if !strings.Contains(err.Error(), "../evasion") {
		t.Errorf("le message ne désigne pas la valeur fautive: %v", err)
	}
}

func TestLogChunkKeysSortChronologically(t *testing.T) {
	var k Keys

	keys := make([]string, 0, 3)
	for _, sequence := range []int{2, 10, 1} {
		key, err := k.TaskLogChunk("run-abc", "unit-test", "step-phpunit", 1, sequence)
		if err != nil {
			t.Fatalf("tranche %d: %v", sequence, err)
		}
		keys = append(keys, key)
	}

	// L'ordre lexicographique doit être l'ordre chronologique : le listing MinIO
	// ne trie pas autrement, et une tranche 10 placée avant la tranche 2
	// produirait un journal mélangé.
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)

	expected := []string{
		"logs/run-abc/unit-test/parts/001-step-phpunit/00000001.log",
		"logs/run-abc/unit-test/parts/001-step-phpunit/00000002.log",
		"logs/run-abc/unit-test/parts/001-step-phpunit/00000010.log",
	}
	for i := range expected {
		if sorted[i] != expected[i] {
			t.Errorf("position %d = %q, attendu %q", i, sorted[i], expected[i])
		}
	}
}

func TestSealedTaskLogAndPrefixes(t *testing.T) {
	var k Keys

	sealed, err := k.TaskLogObject("run-abc", "unit-test")
	if err != nil {
		t.Fatalf("objet scellé: %v", err)
	}
	if sealed != "logs/run-abc/unit-test.log" {
		t.Errorf("objet scellé = %q", sealed)
	}

	// Le préfixe des tranches ne doit pas englober l'objet scellé, sans quoi la
	// purge des tranches emporterait le journal qu'on vient de constituer.
	prefix, err := k.TaskLogPartsPrefix("run-abc", "unit-test")
	if err != nil {
		t.Fatalf("préfixe: %v", err)
	}
	if strings.HasPrefix(sealed, prefix) {
		t.Errorf("l'objet scellé %q tombe sous le préfixe des tranches %q", sealed, prefix)
	}
}

func TestLogChunkRejectsHostileSegments(t *testing.T) {
	var k Keys

	if _, err := k.TaskLogChunk("../evasion", "task", "step", 0, 1); err == nil {
		t.Error("nom de run hostile accepté")
	}
	if _, err := k.TaskLogChunk("run", "task", "step", 0, -1); err == nil {
		t.Error("numéro de tranche négatif accepté")
	}
	if _, err := k.TaskLogChunk("run", "task", "step", -1, 1); err == nil {
		t.Error("rang d'étape négatif accepté")
	}
}

// Le rang de l'étape dans la clé est ce qui rend l'ordre lexicographique
// chronologique.
//
// Le couple choisi n'est pas arbitraire : `publish-results` précède `run-tests`
// dans l'alphabet, alors qu'il s'exécute après. Sans le rang, le journal
// montrerait la publication avant les tests qui la produisent.
func TestStepRankOrdersChunksBeforeContainerName(t *testing.T) {
	var k Keys

	tests, err := k.TaskLogChunk("run-abc", "integration", "step-run-tests", 1, 1)
	if err != nil {
		t.Fatalf("run-tests: %v", err)
	}
	publish, err := k.TaskLogChunk("run-abc", "integration", "step-publish-results", 2, 1)
	if err != nil {
		t.Fatalf("publish-results: %v", err)
	}

	if tests >= publish {
		t.Errorf("ordre inversé:\n  %s\n  %s", tests, publish)
	}

	// Contre-épreuve : à rangs égaux, l'ordre redevient alphabétique, donc faux.
	// Sans cette assertion, le test passerait même si le rang ne servait à rien.
	sameTests, _ := k.TaskLogChunk("run-abc", "integration", "step-run-tests", 0, 1)
	samePublish, _ := k.TaskLogChunk("run-abc", "integration", "step-publish-results", 0, 1)
	if samePublish >= sameTests {
		t.Error("le test ne démontre rien: l'ordre alphabétique est déjà le bon pour ce couple")
	}
}
