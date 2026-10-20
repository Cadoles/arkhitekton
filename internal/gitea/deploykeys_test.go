package gitea

import "testing"

/*
 * Comparing two public keys.
 *
 * It alone decides whether a project's builds start. A false negative would send someone to paste
 * a key that is already set; a false positive would suggest a repository opens when the clone
 * will fail.
 */

const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBDoQT4jgoSvFdwjmhS3GhAn9/48tIieJhlrx1mpZm93"

// The comment does not count: it is free text, and the forge often rewrites it when
// pasted. Comparing whole strings would answer "absent" about a key that is in place.
func TestTheCommentDoesNotDecideAKeysIdentity(t *testing.T) {
	for _, variant := range []string{
		key,
		key + " arkhitekton",
		key + " an entirely different comment",
		"  " + key + " with spaces  ",
	} {
		if !SameKey(variant, key+" arkhitekton-project-mse-git") {
			t.Errorf("variant %q judged different", variant)
		}
	}
}

// Two distinct keys stay distinct, even under the same comment: that is the case of a
// rotation where the old one was not replaced in the repository.
func TestTwoDistinctKeysStayDistinct(t *testing.T) {
	other := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAutreMatiereQuiNestPasLaMeme"

	if SameKey(key+" arkhitekton", other+" arkhitekton") {
		t.Error("two different key materials judged identical")
	}
	// The type counts too: an RSA and an ed25519 are not the same key, even if their
	// material started alike.
	if SameKey("ssh-rsa AAAAB3", "ssh-ed25519 AAAAB3") {
		t.Error("two different key types judged identical")
	}
}

// An entry with no material matches nothing, not even itself.
//
// That is the guard that counts here: a project whose key has not been set carries an
// empty string, and without this rule it would believe itself recognised in a
// repository that declares none. It would then build with a non-existent key, and the
// clone would fail far from the cause.
//
// The comparison does not try to validate a key's syntax beyond that: it is not its
// job, and two well-formed but fanciful strings are allowed to resemble each other.
func TestAnEntryWithNoMaterialMatchesNothing(t *testing.T) {
	for _, withoutMaterial := range []string{"", "   ", "ssh-ed25519"} {
		if SameKey(withoutMaterial, key) {
			t.Errorf("entry %q judged identical to a valid key", withoutMaterial)
		}
		if SameKey(withoutMaterial, withoutMaterial) {
			t.Errorf("entry %q judged identical to itself", withoutMaterial)
		}
	}
}
