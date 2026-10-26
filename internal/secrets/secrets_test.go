package secrets

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// Two distinct keys, produced the way a deployment produces them.
const (
	keyA = "sN0Xk1t2Qv8yZ3aB4cD5eF6gH7iJ8kL9mN0oP1qR2sQ="
	keyB = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
)

func mustCipher(t *testing.T, current string, retired ...string) *Cipher {
	t.Helper()
	c, err := New(current, retired...)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	return c
}

// The round trip, and what the envelope does not show.
func TestASealedValueReopensAndDoesNotRead(t *testing.T) {
	c := mustCipher(t, keyA)

	sealed, err := c.Seal("production-tracker-key")
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}
	if strings.Contains(sealed, "redmine") {
		t.Fatalf("the plaintext shows through in %q", sealed)
	}

	opened, err := c.Open(sealed)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if opened != "production-tracker-key" {
		t.Errorf("opened value = %q", opened)
	}
}

// Two sealings of the same value differ. Without that, comparing two database rows
// would say they carry the same secret — a leak in itself.
func TestTwoSealingsOfTheSameValueDiffer(t *testing.T) {
	c := mustCipher(t, keyA)

	first, _ := c.Seal("identique")
	second, _ := c.Seal("identique")
	if first == second {
		t.Error("two identical envelopes: the nonce does not vary")
	}
}

// Empty stays empty: that is clearing a field, and an envelope would make it look
// filled.
func TestEmptinessIsNotSealed(t *testing.T) {
	c := mustCipher(t, keyA)

	if sealed, err := c.Seal(""); err != nil || sealed != "" {
		t.Errorf("sealing emptiness = %q (%v)", sealed, err)
	}
	if opened, err := c.Open(""); err != nil || opened != "" {
		t.Errorf("opening emptiness = %q (%v)", opened, err)
	}
}

// With no key, the cipher refuses to write rather than letting plaintext through.
func TestWithoutAKeyTheCipherRefusesToWrite(t *testing.T) {
	var absent *Cipher

	if absent.Configured() {
		t.Error("an absent cipher declares itself configured")
	}
	if _, err := absent.Seal("password"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("sealing: %v, expected ErrNotConfigured", err)
	}
	if _, err := absent.Open("v1:abcd:xxxx"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("opening: %v, expected ErrNotConfigured", err)
	}
}

// A badly produced key is found at start-up, not at the first write.
func TestAMalformedKeyIsRefusedAtConstruction(t *testing.T) {
	for _, key := range []string{
		"",
		"not base64!",
		base64.StdEncoding.EncodeToString([]byte("too short")),
	} {
		if _, err := New(key); err == nil {
			t.Errorf("key %q accepted", key)
		}
	}
}

/*
 * The rotation, and the fingerprint's reason for being.
 */

// The old key keeps opening what it had sealed.
func TestARetiredKeyStillOpensWhatItSealed(t *testing.T) {
	previous := mustCipher(t, keyB)
	sealed, err := previous.Seal("secret from before the rotation")
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	// After rotation: one key seals, the other only opens.
	fresh := mustCipher(t, keyA, keyB)
	opened, err := fresh.Open(sealed)
	if err != nil {
		t.Fatalf("opening after rotation: %v", err)
	}
	if opened != "secret from before the rotation" {
		t.Errorf("value = %q", opened)
	}

	// And the cipher can say this value is still to be resealed — without which one
	// would never know when the retired key can leave the configuration.
	if !fresh.NeedsResealing(sealed) {
		t.Error("a value from the old key should ask to be resealed")
	}
	resealed, _ := fresh.Seal("secret from before the rotation")
	if fresh.NeedsResealing(resealed) {
		t.Error("a value from the current key asks for nothing")
	}
}

// A rotation done without keeping the old key says so, and names the missing
// fingerprint: finding it among archived keys would otherwise be blind.
func TestAMissingKeyIsNamedByItsFingerprint(t *testing.T) {
	sealed, _ := mustCipher(t, keyB).Seal("orphan value")

	_, err := mustCipher(t, keyA).Open(sealed)
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("opening: %v, expected ErrUnknownKey", err)
	}

	_, fingerprint, _, _ := splitEnvelope(sealed)
	if !strings.Contains(err.Error(), fingerprint) {
		t.Errorf("message %q — it must name fingerprint %s", err, fingerprint)
	}
}

// An altered envelope is refused, including when only its prefix is: the fingerprint
// is authenticated, so a value is not opened by a key that is not its own.
func TestAnAlteredEnvelopeIsRefused(t *testing.T) {
	c := mustCipher(t, keyA, keyB)
	sealed, _ := c.Seal("intact value")

	version, keyID, payload, ok := splitEnvelope(sealed)
	if !ok {
		t.Fatalf("envelope cannot be decomposed: %q", sealed)
	}

	// Payload modified.
	altered := version + ":" + keyID + ":" + payload[:len(payload)-2] + "AA"
	if _, err := c.Open(altered); err == nil {
		t.Error("modified payload accepted")
	}

	// A prefix doctored towards the other known key.
	_, otherID, _, _ := splitEnvelope(mustSeal(t, mustCipher(t, keyB), "x"))
	if _, err := c.Open(version + ":" + otherID + ":" + payload); err == nil {
		t.Error("doctored fingerprint accepted")
	}

	for _, malformed := range []string{"no-separator", "v1:", "v1::payload", ":abcd:payload"} {
		if _, err := c.Open(malformed); err == nil {
			t.Errorf("envelope %q accepted", malformed)
		}
	}

	// An unknown version: the message states the one this binary reads, failing which a
	// database written by a future version would be diagnosed by guesswork.
	if _, err := c.Open("v2:" + keyID + ":" + payload); err == nil ||
		!strings.Contains(err.Error(), envelopeVersion) {
		t.Errorf("unknown version: %v", err)
	}
}

func mustSeal(t *testing.T, c *Cipher, plaintext string) string {
	t.Helper()
	sealed, err := c.Seal(plaintext)
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}
	return sealed
}
