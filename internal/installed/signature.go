package installed

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
)

/*
 * Signatures in cosign's "simple signing" format, the one `cosign sign --key` writes: a JSON payload
 * naming the signed manifest's digest, stored as a layer of a manifest tagged `sha256-<hex>.sig` next
 * to the artifact, its signature in the layer's annotation.
 *
 * Verified against keys the operator mounts, with no call to sigstore's services: an instance that
 * installs plugins does not depend on a transparency log being reachable, and trusts whom its
 * operator trusts.
 */

const (
	// signatureMediaType is the type of the layer holding the signed payload.
	signatureMediaType = "application/vnd.dev.cosign.simplesigning.v1+json"
	// signatureAnnotation holds the signature, base64, on that layer.
	signatureAnnotation = "dev.cosignproject.cosign/signature"
	// signatureType is the payload's own statement of what it is.
	signatureType = "cosign container image signature"
	// maxPayload bounds a signed payload: a few hundred bytes in practice.
	maxPayload = 64 << 10
)

// TrustedKey is a public key the operator trusts to sign plugins.
type TrustedKey struct {
	// Fingerprint is recorded on each installation it verified: `sha256:<hex>` of the key's DER.
	Fingerprint string
	Key         crypto.PublicKey
}

// ParseTrustedKeys reads PEM public keys, ECDSA or Ed25519.
func ParseTrustedKeys(raw []byte) ([]TrustedKey, error) {
	var keys []TrustedKey
	for {
		block, rest := pem.Decode(raw)
		if block == nil {
			break
		}
		raw = rest
		if block.Type != "PUBLIC KEY" {
			return nil, fmt.Errorf("trusted keys: a %s block, expected PUBLIC KEY", block.Type)
		}
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("trusted keys: %w", err)
		}
		switch key.(type) {
		case *ecdsa.PublicKey, ed25519.PublicKey:
		default:
			return nil, fmt.Errorf("trusted keys: %T, expected ECDSA or Ed25519", key)
		}
		keys = append(keys, TrustedKey{Fingerprint: fingerprint(block.Bytes), Key: key})
	}
	if len(keys) == 0 {
		return nil, errors.New("trusted keys: no PEM public key")
	}
	return keys, nil
}

func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// payload is cosign's simple signing payload.
type payload struct {
	Critical struct {
		Identity struct {
			DockerReference string `json:"docker-reference"`
		} `json:"identity"`
		Image struct {
			DockerManifestDigest string `json:"docker-manifest-digest"`
		} `json:"image"`
		Type string `json:"type"`
	} `json:"critical"`
	Optional map[string]any `json:"optional"`
}

// signaturePayload states that the manifest of this digest, in this repository, is signed.
func signaturePayload(repository, digest string) ([]byte, error) {
	var signed payload
	signed.Critical.Identity.DockerReference = repository
	signed.Critical.Image.DockerManifestDigest = digest
	signed.Critical.Type = signatureType
	return json.Marshal(signed)
}

// sign signs a payload as cosign does: ECDSA over its SHA-256, Ed25519 over the payload itself.
func sign(signer crypto.Signer, signed []byte) ([]byte, error) {
	switch signer.Public().(type) {
	case *ecdsa.PublicKey:
		sum := sha256.Sum256(signed)
		return signer.Sign(rand.Reader, sum[:], crypto.SHA256)
	case ed25519.PublicKey:
		return signer.Sign(rand.Reader, signed, crypto.Hash(0))
	default:
		return nil, fmt.Errorf("signing with %T: ECDSA or Ed25519 only", signer.Public())
	}
}

// verify finds the trusted key that signed the payload, and checks that the payload names the
// digest being installed: a valid signature of another artifact proves nothing about this one.
func verify(trusted []TrustedKey, signed, signature []byte, digest string) (TrustedKey, error) {
	var found *TrustedKey
	for i, candidate := range trusted {
		if checks(candidate.Key, signed, signature) {
			found = &trusted[i]
			break
		}
	}
	if found == nil {
		return TrustedKey{}, errors.New("signed by no trusted key")
	}
	var read payload
	if err := json.Unmarshal(signed, &read); err != nil {
		return TrustedKey{}, fmt.Errorf("signed payload: %w", err)
	}
	if read.Critical.Type != signatureType {
		return TrustedKey{}, fmt.Errorf("signed payload of type %q", read.Critical.Type)
	}
	if read.Critical.Image.DockerManifestDigest != digest {
		return TrustedKey{}, fmt.Errorf("the signature is for %s, not %s", read.Critical.Image.DockerManifestDigest, digest)
	}
	return *found, nil
}

func checks(key crypto.PublicKey, signed, signature []byte) bool {
	switch typed := key.(type) {
	case *ecdsa.PublicKey:
		sum := sha256.Sum256(signed)
		return ecdsa.VerifyASN1(typed, sum[:], signature)
	case ed25519.PublicKey:
		return ed25519.Verify(typed, signed, signature)
	}
	return false
}
