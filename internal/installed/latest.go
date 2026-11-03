package installed

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"oras.land/oras-go/v2/registry"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/version"
)

/*
 * Following a plugin's repository.
 *
 * A plugin is published under its version's tag and installed by digest. Following a repository is
 * reading its tags for a higher version, then resolving that tag to the digest that is installed:
 * the trust chain stays the digest's signature, and a tag only says which digest to look at.
 */

// Source is what an administrator names to install a plugin: a repository, which the plugin then
// follows, or a pinned digest. A tag is resolved to its digest and pins as a digest does.
type Source struct {
	// Repository is `registry/repository`, with neither tag nor digest.
	Repository string
	// Digest pins the plugin; empty, it follows the repository.
	Digest string
	// Tag is the tag named, resolved to Digest by [Resolve].
	Tag string
}

// Pinned says whether the source pins a version rather than following the repository.
func (s Source) Pinned() bool { return s.Digest != "" || s.Tag != "" }

// Reference is the source pinned by its digest, as it is installed.
func (s Source) Reference() string { return s.Repository + "@" + s.Digest }

// ParseSource reads a repository, a tag or a digest.
func ParseSource(raw string) (Source, error) {
	reference, err := registry.ParseReference(strings.TrimSpace(raw))
	if err != nil {
		return Source{}, fmt.Errorf("%s: expected `registry/repository`, with a tag or `@sha256:…` to pin it", raw)
	}
	source := Source{Repository: reference.Registry + "/" + reference.Repository}
	if reference.Reference == "" {
		return source, nil
	}
	if pinned, err := reference.Digest(); err == nil {
		if pinned.Algorithm() != "sha256" {
			return Source{}, fmt.Errorf("%s: a plugin is pinned by a sha256 digest", raw)
		}
		source.Digest = pinned.String()
		return source, nil
	}
	source.Tag = reference.Reference
	return source, nil
}

// Release is a published version of a plugin.
type Release struct {
	Version string
	Digest  string
}

// ErrNoRelease reports a repository with no version to install.
var ErrNoRelease = errors.New("no stable version published")

// Latest returns the highest version a repository publishes above current, and false when there is
// none. Only stable versions count, and the pre-releases above a current pre-release: a plugin
// following its repository is not moved onto a release candidate by surprise. An empty current
// asks for the highest stable version, the one a first installation takes.
func Latest(ctx context.Context, registries Registries, repository, current string) (Release, bool, error) {
	parsed, err := registry.ParseReference(repository)
	if err != nil {
		return Release{}, false, err
	}
	remote, err := registries.repository(parsed)
	if err != nil {
		return Release{}, false, err
	}

	scheme := version.SemVer{}
	prerelease := false
	if current != "" {
		running, err := scheme.Parse(current)
		if err != nil {
			return Release{}, false, fmt.Errorf("current version %q: %w", current, err)
		}
		prerelease = running.Prerelease != ""
	}

	best := ""
	err = remote.Tags(ctx, "", func(tags []string) error {
		for _, tag := range tags {
			candidate, err := scheme.Parse(tag)
			// Other tags are not versions: `latest`, a signature's `sha256-….sig`.
			if err != nil || candidate.Raw != tag || (candidate.Prerelease != "" && !prerelease) {
				continue
			}
			if best == "" || compare(scheme, tag, best) > 0 {
				best = tag
			}
		}
		return nil
	})
	if err != nil {
		return Release{}, false, fmt.Errorf("listing the versions of %s: %w", repository, err)
	}
	if best == "" || (current != "" && compare(scheme, best, current) <= 0) {
		if current == "" {
			return Release{}, false, fmt.Errorf("%s: %w", repository, ErrNoRelease)
		}
		return Release{}, false, nil
	}
	digest, err := Resolve(ctx, registries, repository, best)
	if err != nil {
		return Release{}, false, err
	}
	return Release{Version: best, Digest: digest}, true, nil
}

func compare(scheme version.SemVer, a, b string) int {
	order, _ := scheme.Compare(a, b)
	return order
}

// Resolve returns the digest a repository's tag names.
func Resolve(ctx context.Context, registries Registries, repository, tag string) (string, error) {
	parsed, err := registry.ParseReference(repository + ":" + tag)
	if err != nil {
		return "", err
	}
	remote, err := registries.repository(parsed)
	if err != nil {
		return "", err
	}
	descriptor, err := remote.Resolve(ctx, tag)
	if err != nil {
		return "", fmt.Errorf("resolving %s:%s: %w", repository, tag, err)
	}
	return descriptor.Digest.String(), nil
}

// MajorChange says whether going from one version to another crosses a major version: what the
// screen warns about before an update.
func MajorChange(from, to string) bool {
	major := func(raw string) string {
		parsed, err := version.SemVer{}.Parse(raw)
		if err != nil {
			return raw
		}
		head, _, _ := strings.Cut(parsed.Base, ".")
		return head
	}
	return major(from) != major(to)
}
