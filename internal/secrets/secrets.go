// Package secrets encrypts the sensitive values plugin settings keep in the
// database.
//
// The standing rule — no credential in PostgreSQL — was written for registry
// credentials, and it stays right for them: their consumer is Tekton, which mounts a
// cluster Secret, and the dashboard only names it. It then never needs to read it,
// which its RBAC forbids anyway.
//
// A tracker's API key has another consumer: the dashboard itself. Naming it is not
// enough, it must be presented on every call. Three routes existed, two were ruled
// out:
//
//   - reading it from a cluster Secret — refused, it would need `get` on secrets,
//     exactly the right the posture refuses;
//   - receiving it through the environment, one variable per project — refused, it
//     would make declaring a project depend on a redeployment.
//
// That leaves the database, encrypted. It is a real lowering of protection: a
// database backup becomes a carrier of secrets, and the encryption key must live
// somewhere other than it. Both consequences are accepted.
//
// What this package guarantees: a value read from a database backup says nothing
// without the key, and a replaced key does not make values unreadable — the old one
// keeps decrypting for the duration of a rotation.
//
// It guarantees nothing against someone who obtains both the database and the
// process's environment. It is not a vault: it is what makes a database leak alone
// not a leak of secrets.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ErrNotConfigured reports that no key was supplied.
//
// Distinct from a decryption failure: "the key is missing" is fixed in the
// deployment, "the value is unreadable" means a key was changed without keeping the
// old one. Confusing them would send someone looking in the wrong place.
var ErrNotConfigured = errors.New("no encryption key is configured")

// ErrUnknownKey reports a value sealed by a key this process does not have.
//
// It is the symptom of a rotation done without keeping the previous key. The message
// names the expected fingerprint: without it, finding which archived key fits would
// be a blind search.
var ErrUnknownKey = errors.New("value sealed by a missing key")

// envelopeVersion prefixes every sealed value.
//
// Present from the first version, although there is nothing to distinguish yet: the
// day the construction changes, values already stored must be able to say which
// construction they came from. A format with no version is migrated by guessing, and
// guessing on ciphertext fails silently.
const envelopeVersion = "v1"

// keyLength is the size required: AES-256, and nothing else.
//
// Accepting AES-128 as well would offer a choice nobody wants: the choice of getting
// it wrong. The size being checked at start-up, a badly produced key is found at
// deployment rather than at the first write.
const keyLength = 32

// Cipher seals and opens sensitive values.
//
// The zero value is unusable: an unconstructed Cipher refuses to seal rather than
// returning plaintext. That is the reason for [ErrNotConfigured] — without it, a
// deployment with no key would write readable passwords into the database with
// nothing saying so.
type Cipher struct {
	// sealer is the AEAD of the current key, the one that seals.
	sealer cipher.AEAD
	// sealerID is that key's fingerprint, written into the envelope.
	sealerID string
	// openers holds every known key with its fingerprint: the current one and those kept
	// for the duration of a rotation.
	openers map[string]cipher.AEAD
}

// New builds the cipher from the current key and the retired ones.
//
// Keys are in standard base64, as `openssl rand -base64 32` produces them. An empty
// key among the previous ones is ignored: that is the common case of a variable
// declared but not filled in, and refusing it would force the deployment to delete
// the variable rather than empty it.
func New(current string, retired ...string) (*Cipher, error) {
	block, id, err := aeadFor(current)
	if err != nil {
		return nil, fmt.Errorf("current encryption key: %w", err)
	}

	c := &Cipher{sealer: block, sealerID: id, openers: map[string]cipher.AEAD{id: block}}
	for index, key := range retired {
		if strings.TrimSpace(key) == "" {
			continue
		}
		retiredBlock, retiredID, err := aeadFor(key)
		if err != nil {
			return nil, fmt.Errorf("retired encryption key #%d: %w", index+1, err)
		}
		// The current key wins if it is repeated: sealing with it stays the behaviour, and
		// listing it twice is not a configuration mistake worth refusing to start over.
		if _, known := c.openers[retiredID]; !known {
			c.openers[retiredID] = retiredBlock
		}
	}
	return c, nil
}

// aeadFor decodes a key and derives its fingerprint.
//
// The fingerprint is computed on the key rather than on its rank in the
// configuration. A rank changes when a key is removed from the list, and values
// already sealed would then name another key — making them unreadable at the exact
// moment the rotation believed it was preserving them.
func aeadFor(key string) (cipher.AEAD, string, error) {
	material, err := base64.StdEncoding.DecodeString(strings.TrimSpace(key))
	if err != nil {
		// The message does not quote the value: a badly pasted key is still a key.
		return nil, "", fmt.Errorf("attendue en base64 (openssl rand -base64 32): %w", err)
	}
	if len(material) != keyLength {
		return nil, "", fmt.Errorf("length of %d bytes, expected %d", len(material), keyLength)
	}

	block, err := aes.NewCipher(material)
	if err != nil {
		return nil, "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, "", err
	}

	// Four bytes of the fingerprint are enough to name a key among the few a deployment
	// keeps, and reveal nothing of it: it is a digest, and the key would be needed to
	// reproduce it.
	digest := sha256.Sum256(material)
	return aead, hex.EncodeToString(digest[:4]), nil
}

// Configured says whether a key is available.
//
// It serves callers that must degrade rather than fail: a settings screen must be
// able to say "encryption is not configured" instead of offering a field that will
// refuse to save.
func (c *Cipher) Configured() bool {
	return c != nil && c.sealer != nil
}

// Seal encrypts a value and returns its envelope.
//
// An empty value comes back empty, unencrypted: that is clearing a field, and
// sealing emptiness would produce an envelope indistinguishable from a filled value
// — the screen would say "key saved" over a cleared field.
func (c *Cipher) Seal(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if !c.Configured() {
		return "", ErrNotConfigured
	}

	nonce := make([]byte, c.sealer.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("drawing the nonce: %w", err)
	}

	// The key's fingerprint serves as authenticated data: an envelope whose prefix was
	// doctored to have it opened by another key fails authentication, instead of
	// returning arbitrary bytes.
	sealed := c.sealer.Seal(nonce, nonce, []byte(plaintext), []byte(c.sealerID))
	return envelopeVersion + ":" + c.sealerID + ":" +
		base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Open decrypts an envelope.
//
// An empty string comes back empty: that is the never-filled field, and treating it
// as an error would force every caller to tell it apart itself.
func (c *Cipher) Open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	if !c.Configured() {
		return "", ErrNotConfigured
	}

	version, keyID, payload, ok := splitEnvelope(sealed)
	if !ok {
		return "", errors.New("malformed envelope")
	}
	if version != envelopeVersion {
		return "", fmt.Errorf("envelope of version %q, this binary reads %q",
			version, envelopeVersion)
	}

	aead, known := c.openers[keyID]
	if !known {
		return "", fmt.Errorf("%w: empreinte %s", ErrUnknownKey, keyID)
	}

	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", fmt.Errorf("enveloppe illisible: %w", err)
	}
	if len(raw) < aead.NonceSize() {
		return "", errors.New("truncated envelope")
	}

	nonce, body := raw[:aead.NonceSize()], raw[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, body, []byte(keyID))
	if err != nil {
		// The message does not say why: telling "wrong key" from "altered value" would
		// inform whoever is trying keys.
		return "", errors.New("decryption refused")
	}
	return string(plaintext), nil
}

// NeedsResealing says whether an envelope was sealed by a retired key.
//
// A rotation does not happen at once: values are resealed as they pass through a
// write, and this question says which are not yet. Without it, one would never know
// when the retired key can leave the configuration.
func (c *Cipher) NeedsResealing(sealed string) bool {
	if sealed == "" || !c.Configured() {
		return false
	}
	_, keyID, _, ok := splitEnvelope(sealed)
	return ok && keyID != c.sealerID
}

// splitEnvelope décompose `version:empreinte:charge`.
func splitEnvelope(sealed string) (version, keyID, payload string, ok bool) {
	parts := strings.SplitN(sealed, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}
