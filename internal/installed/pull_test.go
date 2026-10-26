package installed

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"io"
	"io/fs"
	"log"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	orasregistry "oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
)

// registryHost serves a registry in memory, reached over plain HTTP.
func registryHost(t *testing.T) (string, Registries) {
	t.Helper()
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")
	return host, Registries{PlainHTTP: []string{host}}
}

func ecdsaKey(t *testing.T) (*ecdsa.PrivateKey, []TrustedKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key, trust(t, &key.PublicKey)
}

func trust(t *testing.T, public crypto.PublicKey) []TrustedKey {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := ParseTrustedKeys(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	return trusted
}

func TestAPublishedPluginIsPulledAndVerified(t *testing.T) {
	host, registries := registryHost(t)
	key, trusted := ecdsaKey(t)

	reference, err := Publish(context.Background(), registries, host+"/plugins/gotest:1.0.0", plugin(t), key)
	if err != nil {
		t.Fatal(err)
	}
	pulled, err := Pull(context.Background(), registries, trusted, nil, reference)
	if err != nil {
		t.Fatal(err)
	}
	if pinned, digest, err := Pinned(reference); err != nil || pinned != reference || digest != pulled.Digest {
		t.Errorf("pinned %s, %s, %v", pinned, digest, err)
	}
	if pulled.Reference != reference || pulled.SignedBy != trusted[0].Fingerprint || !strings.HasPrefix(pulled.Digest, "sha256:") {
		t.Errorf("pulled = %+v", pulled)
	}
	loaded, err := Load(pulled.Files)
	if err != nil || loaded.ID != "gotest" {
		t.Errorf("loaded %q, %v", loaded.ID, err)
	}

	// The same directory packs to the same layer: a republication changes nothing an administrator
	// reviewed.
	again, err := Publish(context.Background(), registries, host+"/plugins/gotest:again", plugin(t), key)
	if err != nil || again != reference {
		t.Errorf("republished as %s, %v", again, err)
	}
}

func TestEd25519KeysSign(t *testing.T) {
	host, registries := registryHost(t)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := Publish(context.Background(), registries, host+"/plugins/gotest:1.0.0", plugin(t), private)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Pull(context.Background(), registries, trust(t, public), nil, reference); err != nil {
		t.Error(err)
	}
}

func TestAPluginIsInstalledOnlyAsSigned(t *testing.T) {
	ctx := context.Background()
	host, registries := registryHost(t)
	key, trusted := ecdsaKey(t)
	_, stranger := ecdsaKey(t)

	reference, err := Publish(ctx, registries, host+"/plugins/gotest:1.0.0", plugin(t), key)
	if err != nil {
		t.Fatal(err)
	}
	files := plugin(t)
	edit(t, files, manifest.FileName, "version: 1.0.0", "version: 1.0.1")
	other, err := Publish(ctx, registries, host+"/plugins/gotest:1.0.1", files, key)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Pull(ctx, registries, stranger, nil, reference); err == nil || !strings.Contains(err.Error(), "no trusted key") {
		t.Errorf("a stranger's key: %v", err)
	}
	if _, err := Pull(ctx, registries, trusted, nil, host+"/plugins/gotest:1.0.0"); err == nil || !strings.Contains(err.Error(), "by digest") {
		t.Errorf("a tag: %v", err)
	}

	// The signature of 1.0.1, moved onto 1.0.0: valid, by a trusted key, and about another artifact.
	moveSignature(t, host, other, reference)
	if _, err := Pull(ctx, registries, trusted, nil, reference); err == nil || !strings.Contains(err.Error(), "the signature is for") {
		t.Errorf("another artifact's signature: %v", err)
	}

	unsigned := pushForeign(t, host, "plugins/unsigned", ocispec.Manifest{})
	if _, err := Pull(ctx, registries, trusted, nil, unsigned); err == nil || !strings.Contains(err.Error(), "no signature") {
		t.Errorf("unsigned: %v", err)
	}
}

func TestAPulledPluginIsBounded(t *testing.T) {
	ctx := context.Background()
	host, registries := registryHost(t)
	key, trusted := ecdsaKey(t)

	repository := remoteRepository(t, host, "plugins/huge")
	config, err := push(ctx, repository, manifest.ConfigMediaType, []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	huge := ocispec.Descriptor{MediaType: manifest.LayerMediaType, Digest: config.Digest, Size: manifest.MaxSize + 1}
	pushed, err := pushManifest(ctx, repository, ocispec.Manifest{Config: config, Layers: []ocispec.Descriptor{huge}}, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	signArtifact(t, host, "plugins/huge", pushed.String(), key)
	if _, err := Pull(ctx, registries, trusted, nil, host+"/plugins/huge@"+pushed.String()); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("a huge layer: %v", err)
	}

	other := pushForeign(t, host, "plugins/image", ocispec.Manifest{})
	_, digest, _ := strings.Cut(other, "@")
	signArtifact(t, host, "plugins/image", digest, key)
	if _, err := Pull(ctx, registries, trusted, nil, other); err == nil || !strings.Contains(err.Error(), "not an arkhitekton plugin") {
		t.Errorf("not a plugin: %v", err)
	}
}

func TestALayerUnpacksOnlyPlainFiles(t *testing.T) {
	for name, header := range map[string]*tar.Header{
		"a link":      {Name: "tekton/link.yaml", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		"a way out":   {Name: "../escape.yaml", Typeflag: tar.TypeReg, Size: 1},
		"an absolute": {Name: "/etc/cron.d/x", Typeflag: tar.TypeReg, Size: 1},
		"a device":    {Name: "dev", Typeflag: tar.TypeChar},
	} {
		var buffer bytes.Buffer
		compressed := gzip.NewWriter(&buffer)
		archive := tar.NewWriter(compressed)
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			_, _ = archive.Write([]byte("x"))
		}
		_ = archive.Close()
		_ = compressed.Close()
		if _, err := Unpack(&buffer, manifest.MaxSize); err == nil {
			t.Errorf("%s unpacked", name)
		}
	}

	layer, err := Pack(plugin(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Unpack(bytes.NewReader(layer), 1024); err == nil {
		t.Error("a layer larger than the limit unpacked")
	}
	files, err := Unpack(bytes.NewReader(layer), manifest.MaxSize)
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := fs.ReadFile(files, "tekton/task-go-test.yaml"); err != nil || !bytes.Contains(raw, []byte("go test -json")) {
		t.Errorf("unpacked %q, %v", raw, err)
	}
}

func remoteRepository(t *testing.T, host, name string) *remote.Repository {
	t.Helper()
	reference, err := orasregistry.ParseReference(host + "/" + name)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := Registries{PlainHTTP: []string{host}}.repository(reference)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

// pushForeign pushes an artifact that is not a plugin, tagged latest, and returns its reference by
// digest.
func pushForeign(t *testing.T, host, name string, built ocispec.Manifest) string {
	t.Helper()
	ctx := context.Background()
	repository := remoteRepository(t, host, name)
	config, err := push(ctx, repository, ocispec.MediaTypeImageConfig, []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	layer, err := push(ctx, repository, ocispec.MediaTypeImageLayerGzip, []byte("not a plugin"))
	if err != nil {
		t.Fatal(err)
	}
	built.Config, built.Layers = config, []ocispec.Descriptor{layer}
	pushed, err := pushManifest(ctx, repository, built, "latest")
	if err != nil {
		t.Fatal(err)
	}
	return host + "/" + name + "@" + pushed.String()
}

// signArtifact signs a digest as Publish would.
func signArtifact(t *testing.T, host, name, digest string, signer crypto.Signer) {
	t.Helper()
	ctx := context.Background()
	repository := remoteRepository(t, host, name)
	signed, err := signaturePayload(host+"/"+name, digest)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := sign(signer, signed)
	if err != nil {
		t.Fatal(err)
	}
	layer, err := push(ctx, repository, signatureMediaType, signed)
	if err != nil {
		t.Fatal(err)
	}
	layer.Annotations = map[string]string{signatureAnnotation: base64.StdEncoding.EncodeToString(signature)}
	config, err := push(ctx, repository, ocispec.MediaTypeImageConfig, []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	tag := strings.Replace(digest, ":", "-", 1) + ".sig"
	if _, err := pushManifest(ctx, repository, ocispec.Manifest{Config: config, Layers: []ocispec.Descriptor{layer}}, tag); err != nil {
		t.Fatal(err)
	}
}

// moveSignature copies the signature of one artifact onto the tag where another's is looked for.
func moveSignature(t *testing.T, host, from, to string) {
	t.Helper()
	ctx := context.Background()
	repository := remoteRepository(t, host, "plugins/gotest")
	_, fromDigest, _ := strings.Cut(from, "@")
	_, toDigest, _ := strings.Cut(to, "@")
	descriptor, body, err := repository.FetchReference(ctx, strings.Replace(fromDigest, ":", "-", 1)+".sig")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := readBlob(body, descriptor, maxManifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PushReference(ctx, descriptor, strings.NewReader(raw), strings.Replace(toDigest, ":", "-", 1)+".sig"); err != nil {
		t.Fatal(err)
	}
}

// A plugin signed by no trusted key installs only if the operator named it.
func TestAnUnsignedPluginInstallsOnlyWhenNamed(t *testing.T) {
	host, registries := registryHost(t)
	stranger, _ := ecdsaKey(t)
	_, trusted := ecdsaKey(t)
	reference, err := Publish(context.Background(), registries, host+"/plugins/gotest:1.0.0", plugin(t), stranger)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := Pull(ctx, registries, trusted, []string{"other"}, reference); err == nil || !strings.Contains(err.Error(), `"gotest" is not one`) {
		t.Errorf("another plugin named: %v", err)
	}
	pulled, err := Pull(ctx, registries, nil, []string{"gotest"}, reference)
	if err != nil {
		t.Fatal(err)
	}
	if pulled.SignedBy != Unsigned {
		t.Errorf("signed by %q", pulled.SignedBy)
	}
	if loaded, err := Load(pulled.Files); err != nil || loaded.ID != "gotest" {
		t.Errorf("loaded %v", err)
	}
}
