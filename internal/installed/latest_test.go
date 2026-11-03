package installed

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
)

// publishVersion publishes the example plugin as a version, under that version's tag.
func publishVersion(t *testing.T, host string, registries Registries, release string) string {
	t.Helper()
	key, _ := ecdsaKey(t)
	files := publishable(t)
	edit(t, files, manifest.FileName, "version: 1.0.0", "version: "+release)
	reference, err := Publish(context.Background(), registries, host+"/plugins/gotest:"+release, files, key)
	if err != nil {
		t.Fatal(err)
	}
	return reference
}

func TestTheLatestStableVersionIsTheOneOffered(t *testing.T) {
	host, registries := registryHost(t)
	repository := host + "/plugins/gotest"
	publishVersion(t, host, registries, "1.0.0")
	stable := publishVersion(t, host, registries, "1.2.0")
	publishVersion(t, host, registries, "1.1.0")
	publishVersion(t, host, registries, "1.3.0-rc1")

	release, found, err := Latest(context.Background(), registries, repository, "1.0.0")
	if err != nil || !found {
		t.Fatalf("found %v, %v", found, err)
	}
	if release.Version != "1.2.0" || repository+"@"+release.Digest != stable {
		t.Errorf("release = %+v, expected 1.2.0 at %s", release, stable)
	}

	// A first installation takes the highest stable version.
	if first, _, err := Latest(context.Background(), registries, repository, ""); err != nil || first.Version != "1.2.0" {
		t.Errorf("first = %+v, %v", first, err)
	}
	// Up to date: nothing to offer.
	if _, found, err := Latest(context.Background(), registries, repository, "1.2.0"); err != nil || found {
		t.Errorf("up to date: found %v, %v", found, err)
	}
	// On a pre-release, the next pre-releases are offered too.
	if next, found, err := Latest(context.Background(), registries, repository, "1.3.0-rc0"); err != nil || !found || next.Version != "1.3.0-rc1" {
		t.Errorf("from a pre-release: %+v, %v, %v", next, found, err)
	}
}

func TestARepositoryWithNoStableVersionHasNothingToInstall(t *testing.T) {
	host, registries := registryHost(t)
	publishVersion(t, host, registries, "1.0.0-rc1")
	if _, _, err := Latest(context.Background(), registries, host+"/plugins/gotest", ""); !errors.Is(err, ErrNoRelease) {
		t.Errorf("err = %v", err)
	}
}

// What a following instance compares is the tag: it must be the version the plugin carries.
func TestAVersionTagMustBeThePluginsVersion(t *testing.T) {
	host, registries := registryHost(t)
	key, _ := ecdsaKey(t)
	_, err := Publish(context.Background(), registries, host+"/plugins/gotest:1.0.1", publishable(t), key)
	if err == nil || !strings.Contains(err.Error(), "1.0.0") {
		t.Errorf("err = %v", err)
	}
}

func TestASourceFollowsOrPins(t *testing.T) {
	for raw, expected := range map[string]Source{
		"forge.example/org/plugin":       {Repository: "forge.example/org/plugin"},
		"forge.example/org/plugin:2.4.0": {Repository: "forge.example/org/plugin", Tag: "2.4.0"},
		"forge.example/org/plugin@sha256:" + strings.Repeat("a", 64): {
			Repository: "forge.example/org/plugin", Digest: "sha256:" + strings.Repeat("a", 64)},
	} {
		source, err := ParseSource(raw)
		if err != nil || source != expected {
			t.Errorf("%s = %+v, %v", raw, source, err)
		}
	}
	if _, err := ParseSource("plugin"); err == nil {
		t.Error("a name with no registry is not a repository")
	}
	if !MajorChange("1.9.0", "2.0.0") || MajorChange("2.4.0", "2.5.1") {
		t.Error("major change")
	}
}
