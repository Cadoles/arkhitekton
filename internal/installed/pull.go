package installed

import (
	"bytes"
	"context"
	"crypto"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"runtime"
	"strings"
	"testing/fstest"

	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
	"sigs.k8s.io/yaml"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
)

/*
 * Pulling a plugin from an OCI registry, and publishing one.
 *
 * Pinned by digest: a tag can be moved, and what an administrator reviewed must be what is
 * installed, now and when a replica pulls it again. The signature is verified before the layer is
 * even fetched.
 */

const (
	// maxManifest bounds a manifest, the artifact's or its signature's.
	maxManifest = 4 << 20
	// maxSignatures bounds the signatures read on one artifact.
	maxSignatures = 16
)

// Registries says how registries are reached.
type Registries struct {
	// DockerConfig is a mounted `config.json` holding the credentials of private registries. Empty,
	// registries are reached anonymously. Its `auths` are read; credential helpers are not run.
	DockerConfig string
	// PlainHTTP lists the registries reached without TLS, a registry inside a development cluster.
	PlainHTTP []string
	// Architecture is the platform whose program is pulled; empty, this process's.
	Architecture string
}

// Platforms a plugin's program is published for, the dashboard's images' architectures.
var Platforms = []string{"amd64", "arm64"}

func (r Registries) architecture() string {
	if r.Architecture != "" {
		return r.Architecture
	}
	return runtime.GOARCH
}

func (r Registries) repository(reference registry.Reference) (*remote.Repository, error) {
	repository, err := remote.NewRepository(reference.Registry + "/" + reference.Repository)
	if err != nil {
		return nil, err
	}
	for _, host := range r.PlainHTTP {
		if host == reference.Registry {
			repository.PlainHTTP = true
		}
	}
	client := &auth.Client{Cache: auth.NewCache()}
	if r.DockerConfig != "" {
		store, err := credentials.NewFileStore(r.DockerConfig)
		if err != nil {
			return nil, fmt.Errorf("registry credentials: %w", err)
		}
		client.Credential = credentials.Credential(store)
	}
	repository.Client = client
	return repository, nil
}

// Pulled is a plugin fetched and verified.
type Pulled struct {
	// Reference is the repository and digest it was pulled from.
	Reference string
	Digest    string
	// SignedBy is the fingerprint of the trusted key that signed it.
	SignedBy string
	// Layer is the directory as packed and signed: what is stored, and unpacked at each load.
	Layer []byte
	Files fs.FS
}

// Pinned reads a reference that pins a plugin by digest, and returns it as `repository@digest`.
func Pinned(raw string) (reference, pinned string, err error) {
	parsed, _, err := pin(raw)
	if err != nil {
		return "", "", err
	}
	return parsed.Registry + "/" + parsed.Repository + "@" + parsed.Reference, parsed.Reference, nil
}

func pin(raw string) (registry.Reference, digest.Digest, error) {
	reference, err := registry.ParseReference(strings.TrimSpace(raw))
	if err != nil {
		return registry.Reference{}, "", err
	}
	pinned, err := reference.Digest()
	if err != nil || pinned.Algorithm() != digest.SHA256 {
		return registry.Reference{}, "", fmt.Errorf("%s: a plugin is installed by digest, `repository@sha256:...`", raw)
	}
	return reference, pinned, nil
}

// Pull fetches the plugin a reference pins, checks that a trusted key signed it, and unpacks it.
func Pull(ctx context.Context, registries Registries, trusted []TrustedKey, raw string) (Pulled, error) {
	reference, pinned, err := pin(raw)
	if err != nil {
		return Pulled{}, err
	}
	repository, err := registries.repository(reference)
	if err != nil {
		return Pulled{}, err
	}

	descriptor, body, err := repository.FetchReference(ctx, pinned.String())
	if err != nil {
		return Pulled{}, fmt.Errorf("fetching %s: %w", raw, err)
	}
	artifactJSON, err := readBlob(body, descriptor, maxManifest)
	if err != nil {
		return Pulled{}, err
	}
	signer, err := verified(ctx, repository, trusted, pinned)
	if err != nil {
		return Pulled{}, fmt.Errorf("%s: %w", reference, err)
	}

	// A plugin with a program is an index, one artifact per platform. The signature covers the
	// index, and the index names each artifact by its digest.
	if descriptor.MediaType == ocispec.MediaTypeImageIndex {
		if artifactJSON, err = platformArtifact(ctx, repository, artifactJSON, registries.architecture()); err != nil {
			return Pulled{}, fmt.Errorf("%s: %w", reference, err)
		}
	}
	var artifact ocispec.Manifest
	if err := json.Unmarshal([]byte(artifactJSON), &artifact); err != nil {
		return Pulled{}, fmt.Errorf("artifact manifest: %w", err)
	}
	if artifact.Config.MediaType != manifest.ConfigMediaType || len(artifact.Layers) != 1 ||
		artifact.Layers[0].MediaType != manifest.LayerMediaType {
		return Pulled{}, fmt.Errorf("%s is not an arkhitekton plugin", reference)
	}
	layerDescriptor := artifact.Layers[0]
	if layerDescriptor.Size > manifest.MaxSize {
		return Pulled{}, fmt.Errorf("plugin layer of %d bytes, larger than %d MiB", layerDescriptor.Size, manifest.MaxSize>>20)
	}
	layerBody, err := repository.Fetch(ctx, layerDescriptor)
	if err != nil {
		return Pulled{}, fmt.Errorf("fetching the plugin layer: %w", err)
	}
	layer, err := readBlob(layerBody, layerDescriptor, manifest.MaxSize)
	if err != nil {
		return Pulled{}, err
	}
	files, err := Unpack(strings.NewReader(layer), manifest.MaxSize)
	if err != nil {
		return Pulled{}, err
	}
	return Pulled{
		Reference: reference.Registry + "/" + reference.Repository + "@" + pinned.String(),
		Digest:    pinned.String(), SignedBy: signer.Fingerprint, Layer: []byte(layer), Files: files,
	}, nil
}

// platformArtifact reads the artifact of an index built for an architecture.
func platformArtifact(ctx context.Context, repository *remote.Repository, indexJSON, architecture string) (string, error) {
	var index ocispec.Index
	if err := json.Unmarshal([]byte(indexJSON), &index); err != nil {
		return "", fmt.Errorf("plugin index: %w", err)
	}
	for _, entry := range index.Manifests {
		if entry.Platform == nil || entry.Platform.OS != "linux" || entry.Platform.Architecture != architecture {
			continue
		}
		body, err := repository.Fetch(ctx, entry)
		if err != nil {
			return "", fmt.Errorf("fetching the linux/%s artifact: %w", architecture, err)
		}
		return readBlob(body, entry, maxManifest)
	}
	return "", fmt.Errorf("no program built for linux/%s", architecture)
}

// readBlob reads a blob whole, its size and digest checked against its descriptor.
func readBlob(body io.ReadCloser, descriptor ocispec.Descriptor, limit int64) (string, error) {
	defer func() { _ = body.Close() }()
	if descriptor.Size > limit {
		return "", fmt.Errorf("%s: %d bytes, more than %d", descriptor.Digest, descriptor.Size, limit)
	}
	raw, err := content.ReadAll(body, descriptor)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// signatureTag is where cosign stores the signatures of a manifest.
func signatureTag(pinned digest.Digest) string {
	return strings.Replace(pinned.String(), ":", "-", 1) + ".sig"
}

// verified finds a signature of the digest by a trusted key.
func verified(ctx context.Context, repository *remote.Repository, trusted []TrustedKey, pinned digest.Digest) (TrustedKey, error) {
	descriptor, body, err := repository.FetchReference(ctx, signatureTag(pinned))
	if err != nil {
		return TrustedKey{}, fmt.Errorf("no signature: %w", err)
	}
	raw, err := readBlob(body, descriptor, maxManifest)
	if err != nil {
		return TrustedKey{}, err
	}
	var signatures ocispec.Manifest
	if err := json.Unmarshal([]byte(raw), &signatures); err != nil {
		return TrustedKey{}, fmt.Errorf("signature manifest: %w", err)
	}

	var problems []error
	for i, layer := range signatures.Layers {
		if i == maxSignatures {
			break
		}
		encoded, signed := layer.Annotations[signatureAnnotation]
		if layer.MediaType != signatureMediaType || !signed {
			continue
		}
		signature, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			problems = append(problems, fmt.Errorf("signature %d: %w", i, err))
			continue
		}
		body, err := repository.Fetch(ctx, layer)
		if err != nil {
			problems = append(problems, fmt.Errorf("signature %d: %w", i, err))
			continue
		}
		payload, err := readBlob(body, layer, maxPayload)
		if err != nil {
			problems = append(problems, fmt.Errorf("signature %d: %w", i, err))
			continue
		}
		key, err := verify(trusted, []byte(payload), signature, pinned.String())
		if err == nil {
			return key, nil
		}
		problems = append(problems, fmt.Errorf("signature %d: %w", i, err))
	}
	if len(problems) == 0 {
		return TrustedKey{}, errors.New("no signature")
	}
	return TrustedKey{}, errors.Join(problems...)
}

// Publish packs a plugin's directory, pushes it under a tag, and signs what it pushed. It returns
// the reference by digest an administrator installs.
//
// A plugin with a program (`binary: bin/plugin`) carries it built for each platform beside that
// path, `bin/plugin-linux-amd64` and `bin/plugin-linux-arm64`: each becomes the artifact of its
// platform, holding the program at the declared path, and an index of them is what is signed.
func Publish(ctx context.Context, registries Registries, target string, fsys fs.FS, signer crypto.Signer) (string, error) {
	reference, err := registry.ParseReference(target)
	if err != nil {
		return "", err
	}
	if err := reference.ValidateReferenceAsTag(); err != nil {
		return "", fmt.Errorf("%s: published under a tag, installed by the digest this returns", target)
	}
	built, err := platformDirectories(fsys)
	if err != nil {
		return "", err
	}
	for platform, files := range built {
		if _, err := Load(files); err != nil {
			return "", fmt.Errorf("the plugin would not install%s: %w", platform, err)
		}
	}
	repository, err := registries.repository(reference)
	if err != nil {
		return "", err
	}

	var pushed digest.Digest
	if files, single := built[""]; single {
		descriptor, err := pushArtifact(ctx, repository, files)
		if err != nil {
			return "", err
		}
		if pushed, err = pushManifest(ctx, repository, descriptor.manifest, reference.Reference); err != nil {
			return "", err
		}
	} else {
		var index ocispec.Index
		for _, architecture := range Platforms {
			files, found := built[" for linux/"+architecture]
			if !found {
				continue
			}
			artifact, err := pushArtifact(ctx, repository, files)
			if err != nil {
				return "", err
			}
			raw, err := json.Marshal(artifact.manifest)
			if err != nil {
				return "", err
			}
			entry := content.NewDescriptorFromBytes(ocispec.MediaTypeImageManifest, raw)
			if err := repository.Push(ctx, entry, bytes.NewReader(raw)); err != nil {
				return "", fmt.Errorf("pushing the linux/%s artifact: %w", architecture, err)
			}
			entry.Platform = &ocispec.Platform{OS: "linux", Architecture: architecture}
			index.Manifests = append(index.Manifests, entry)
		}
		if pushed, err = pushIndex(ctx, repository, index, reference.Reference); err != nil {
			return "", err
		}
	}

	repositoryName := reference.Registry + "/" + reference.Repository
	signed, err := signaturePayload(repositoryName, pushed.String())
	if err != nil {
		return "", err
	}
	signature, err := sign(signer, signed)
	if err != nil {
		return "", err
	}
	payloadDescriptor, err := push(ctx, repository, signatureMediaType, signed)
	if err != nil {
		return "", err
	}
	payloadDescriptor.Annotations = map[string]string{signatureAnnotation: base64.StdEncoding.EncodeToString(signature)}
	emptyConfig, err := push(ctx, repository, ocispec.MediaTypeImageConfig, []byte("{}"))
	if err != nil {
		return "", err
	}
	if _, err := pushManifest(ctx, repository, ocispec.Manifest{
		Config: emptyConfig, Layers: []ocispec.Descriptor{payloadDescriptor},
	}, signatureTag(pushed)); err != nil {
		return "", err
	}
	return repositoryName + "@" + pushed.String(), nil
}

// platformDirectories returns the directory as each platform's artifact holds it, keyed by a label
// for messages: one, keyed "", for a plugin without a program.
func platformDirectories(fsys fs.FS) (map[string]fs.FS, error) {
	raw, err := fs.ReadFile(fsys, manifest.FileName)
	if err != nil {
		return nil, fmt.Errorf("plugin manifest: %w", err)
	}
	converted, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("plugin manifest: %w", err)
	}
	var declared struct {
		Binary string `json:"binary"`
	}
	if err := json.Unmarshal(converted, &declared); err != nil {
		return nil, fmt.Errorf("plugin manifest: %w", err)
	}
	if declared.Binary == "" {
		return map[string]fs.FS{"": fsys}, nil
	}

	built := map[string]fs.FS{}
	for _, architecture := range Platforms {
		program, err := fs.ReadFile(fsys, declared.Binary+"-linux-"+architecture)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		files := fstest.MapFS{declared.Binary: {Data: program, Mode: 0o755}}
		err = fs.WalkDir(fsys, ".", func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || path == declared.Binary || strings.HasPrefix(path, declared.Binary+"-linux-") {
				return err
			}
			data, err := fs.ReadFile(fsys, path)
			files[path] = &fstest.MapFile{Data: data}
			return err
		})
		if err != nil {
			return nil, err
		}
		built[" for linux/"+architecture] = files
	}
	if len(built) == 0 {
		return nil, fmt.Errorf("no program built: %s-linux-<architecture>, for %s", declared.Binary, strings.Join(Platforms, " or "))
	}
	return built, nil
}

type artifact struct{ manifest ocispec.Manifest }

// pushArtifact pushes a directory's config and layer, and returns the manifest that names them.
func pushArtifact(ctx context.Context, repository *remote.Repository, fsys fs.FS) (artifact, error) {
	layer, err := Pack(fsys)
	if err != nil {
		return artifact{}, err
	}
	config, err := push(ctx, repository, manifest.ConfigMediaType, []byte("{}"))
	if err != nil {
		return artifact{}, err
	}
	layerDescriptor, err := push(ctx, repository, manifest.LayerMediaType, layer)
	if err != nil {
		return artifact{}, err
	}
	return artifact{manifest: ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageManifest,
		Config: config, Layers: []ocispec.Descriptor{layerDescriptor},
	}}, nil
}

func pushIndex(ctx context.Context, repository *remote.Repository, index ocispec.Index, tag string) (digest.Digest, error) {
	index.SchemaVersion = 2
	index.MediaType = ocispec.MediaTypeImageIndex
	raw, err := json.Marshal(index)
	if err != nil {
		return "", err
	}
	descriptor := content.NewDescriptorFromBytes(ocispec.MediaTypeImageIndex, raw)
	if err := repository.PushReference(ctx, descriptor, bytes.NewReader(raw), tag); err != nil {
		return "", fmt.Errorf("pushing the index: %w", err)
	}
	return descriptor.Digest, nil
}

func push(ctx context.Context, repository *remote.Repository, mediaType string, raw []byte) (ocispec.Descriptor, error) {
	descriptor := content.NewDescriptorFromBytes(mediaType, raw)
	if err := repository.Push(ctx, descriptor, bytes.NewReader(raw)); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("pushing %s: %w", mediaType, err)
	}
	return descriptor, nil
}

func pushManifest(ctx context.Context, repository *remote.Repository, built ocispec.Manifest, tag string) (digest.Digest, error) {
	built.SchemaVersion = 2
	built.MediaType = ocispec.MediaTypeImageManifest
	raw, err := json.Marshal(built)
	if err != nil {
		return "", err
	}
	descriptor := content.NewDescriptorFromBytes(ocispec.MediaTypeImageManifest, raw)
	if err := repository.PushReference(ctx, descriptor, bytes.NewReader(raw), tag); err != nil {
		return "", fmt.Errorf("pushing the manifest: %w", err)
	}
	return descriptor.Digest, nil
}
