package app

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"oras.land/oras-go/v2/registry"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/installed"
)

// runPublishPlugin packs a plugin's directory, pushes it to a registry under a tag and signs it the
// way cosign does, for an author who has no cosign: what an administrator then installs by the
// digest it prints.
func runPublishPlugin(args []string) error {
	flags := flag.NewFlagSet("publish-plugin", flag.ContinueOnError)
	dir := flags.String("dir", "", "the plugin's directory, arkhitekton-plugin.yaml at its root")
	target := flags.String("ref", "", "where to push it: registry/repository:tag")
	keyPath := flags.String("key", "", "the PEM private key that signs it, ECDSA or Ed25519, unencrypted")
	plainHTTP := flags.Bool("plain-http", false, "reach the registry without TLS")
	dockerConfig := flags.String("docker-config", defaultDockerConfig(), "the Docker config.json holding the registry's credentials")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dir == "" || *target == "" || *keyPath == "" {
		return errors.New("usage: arkhitekton publish-plugin --dir <plugin> --ref <registry/repository:tag> --key <key.pem>")
	}

	signer, err := readSigner(*keyPath)
	if err != nil {
		return err
	}
	registries := installed.Registries{}
	if _, err := os.Stat(*dockerConfig); *dockerConfig != "" && err == nil {
		registries.DockerConfig = *dockerConfig
	}
	if *plainHTTP {
		reference, err := registry.ParseReference(*target)
		if err != nil {
			return err
		}
		registries.PlainHTTP = []string{reference.Registry}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pinned, err := installed.Publish(ctx, registries, *target, os.DirFS(*dir), signer)
	if err != nil {
		return err
	}
	signedBy, err := installed.Fingerprint(signer.Public())
	if err != nil {
		return err
	}
	fmt.Printf("published  %s\nsigned by  %s\n", pinned, signedBy)
	return nil
}

func defaultDockerConfig() string {
	if dir := os.Getenv("DOCKER_CONFIG"); dir != "" {
		return filepath.Join(dir, "config.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".docker", "config.json")
}

// readSigner reads an unencrypted private key, PKCS #8 or SEC 1.
func readSigner(path string) (crypto.Signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("%s: no PEM block", path)
	}
	switch {
	case strings.Contains(block.Type, "ENCRYPTED"):
		return nil, fmt.Errorf("%s: an encrypted key; export it unencrypted, or sign with cosign itself", path)
	case block.Type == "EC PRIVATE KEY":
		return x509.ParseECPrivateKey(block.Bytes)
	case block.Type == "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		signer, ok := key.(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("%s: a %T cannot sign", path, key)
		}
		return signer, nil
	default:
		return nil, fmt.Errorf("%s: a %s block, expected a private key", path, block.Type)
	}
}
