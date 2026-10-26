package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/installed"
)

// The example plugin, built and published as its author would, installs: pulled by the digest the
// command prints, verified against the public half of the key it was given.
func TestTheExamplePluginPublishes(t *testing.T) {
	dir := builtExample(t)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")

	output := captureStdout(t, func() {
		if err := runPublishPlugin([]string{"--dir", dir, "--ref", host + "/plugins/gotest:1.0.0",
			"--key", keyPath, "--plain-http", "--docker-config", ""}); err != nil {
			t.Fatal(err)
		}
	})
	reference := strings.TrimSpace(strings.TrimPrefix(strings.SplitN(output, "\n", 2)[0], "published"))

	public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := installed.ParseTrustedKeys(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: public}))
	if err != nil {
		t.Fatal(err)
	}
	pulled, err := installed.Pull(context.Background(), installed.Registries{PlainHTTP: []string{host}}, trusted, nil, reference)
	if err != nil {
		t.Fatalf("pulling %q: %v", reference, err)
	}
	if !strings.Contains(output, pulled.SignedBy) {
		t.Errorf("output %q does not name the key %s", output, pulled.SignedBy)
	}
}

func TestAnEncryptedKeyIsRefusedWithItsReason(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cosign.key")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED SIGSTORE PRIVATE KEY", Bytes: []byte("{}")}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSigner(path); err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Errorf("err = %v", err)
	}
}

func captureStdout(t *testing.T, run func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = writer
	run()
	os.Stdout = stdout
	_ = writer.Close()
	output, _ := io.ReadAll(reader)
	return string(output)
}
