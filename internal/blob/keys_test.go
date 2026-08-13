package blob

import (
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
