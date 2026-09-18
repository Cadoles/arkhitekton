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

		{"step log", func() (string, error) {
			return k.TaskLog("php-integration-test-run-1", "run-integration-tests", "step-0")
		}, "logs/php-integration-test-run-1/run-integration-tests/step-0.log"},

		{"release deliverable", func() (string, error) {
			return k.ReleaseArtifact("1.8.0-rc3", "delivery-note.pdf")
		}, "artifacts/releases/1.8.0-rc3/delivery-note.pdf"},

		{"run prefix", func() (string, error) {
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
			t.Errorf("%s = %q, expected %q", c.name, got, c.expected)
		}
	}
}

// Release tags are typed by a user. S3 has no notion of directory to object to
// "../": the key is a free string, and validation is the only barrier.
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
		"tag with spaces",
		"-starts-with-a-dash",
		"tag\nmultiligne",
	}

	for _, segment := range hostile {
		if key, err := k.ReleaseArtifact(segment, "note.pdf"); err == nil {
			t.Errorf("hostile segment %q accepted, key produced: %q", segment, key)
		}
		if key, err := k.RawArtifact("run-ok", segment); err == nil {
			t.Errorf("hostile artifact name %q accepted, key produced: %q", segment, key)
		}
	}
}

// A key produced must never leave its prefix, whatever input is accepted.
func TestValidKeysStayWithinTheirPrefix(t *testing.T) {
	var k Keys

	valid := []string{"1.8.0", "1.8.0-rc3", "run-abc-123", "a_b.c-d", "X"}

	for _, segment := range valid {
		key, err := k.ReleaseArtifact(segment, "note.pdf")
		if err != nil {
			t.Errorf("legitimate segment %q refused: %v", segment, err)
			continue
		}
		if !strings.HasPrefix(key, "artifacts/releases/") {
			t.Errorf("key %q left its prefix", key)
		}
		if strings.Contains(key, "..") {
			t.Errorf("key %q contains a traversal", key)
		}
	}
}

func TestUnsafeSegmentErrorNamesTheOffendingValue(t *testing.T) {
	var k Keys

	_, err := k.ReleaseArtifact("../evasion", "note.pdf")
	if err == nil {
		t.Fatal("hostile segment accepted")
	}
	if !strings.Contains(err.Error(), "../evasion") {
		t.Errorf("the message does not name the offending value: %v", err)
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

	// Lexicographic order must be chronological order: the store's listing sorts no
	// other way, and a chunk 10 placed before chunk 2 would produce a jumbled log.
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)

	expected := []string{
		"logs/run-abc/unit-test/parts/001-step-phpunit/00000001.log",
		"logs/run-abc/unit-test/parts/001-step-phpunit/00000002.log",
		"logs/run-abc/unit-test/parts/001-step-phpunit/00000010.log",
	}
	for i := range expected {
		if sorted[i] != expected[i] {
			t.Errorf("position %d = %q, expected %q", i, sorted[i], expected[i])
		}
	}
}

func TestSealedTaskLogAndPrefixes(t *testing.T) {
	var k Keys

	sealed, err := k.TaskLogObject("run-abc", "unit-test")
	if err != nil {
		t.Fatalf("sealed object: %v", err)
	}
	if sealed != "logs/run-abc/unit-test.log" {
		t.Errorf("sealed object = %q", sealed)
	}

	// The chunk prefix must not encompass the sealed object, otherwise purging the
	// chunks would take away the log just assembled.
	prefix, err := k.TaskLogPartsPrefix("run-abc", "unit-test")
	if err != nil {
		t.Fatalf("prefix: %v", err)
	}
	if strings.HasPrefix(sealed, prefix) {
		t.Errorf("the sealed object %q falls under the chunk prefix %q", sealed, prefix)
	}
}

func TestLogChunkRejectsHostileSegments(t *testing.T) {
	var k Keys

	if _, err := k.TaskLogChunk("../evasion", "task", "step", 0, 1); err == nil {
		t.Error("hostile run name accepted")
	}
	if _, err := k.TaskLogChunk("run", "task", "step", 0, -1); err == nil {
		t.Error("negative chunk number accepted")
	}
	if _, err := k.TaskLogChunk("run", "task", "step", -1, 1); err == nil {
		t.Error("negative step rank accepted")
	}
}

// The step's rank in the key is what makes lexicographic order chronological.
//
// The pair chosen is not arbitrary: `publish-results` precedes `run-tests`
// alphabetically, while it runs afterwards. Without the rank, the log would show
// publishing before the tests that produce it.
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
		t.Errorf("reversed order:\n  %s\n  %s", tests, publish)
	}

	// The counter-proof: at equal ranks the order becomes alphabetical again, hence
	// wrong. Without this assertion the test would pass even if the rank did nothing.
	sameTests, _ := k.TaskLogChunk("run-abc", "integration", "step-run-tests", 0, 1)
	samePublish, _ := k.TaskLogChunk("run-abc", "integration", "step-publish-results", 0, 1)
	if samePublish >= sameTests {
		t.Error("the test proves nothing: alphabetical order is already right for this pair")
	}
}
