// Package images reads the references of published images.
//
// Nothing more: which images a project publishes is declared by the build steps of its release
// plan, and their references are what the builds returned. What remains here is how to read
// `registry/name:tag`, and to name the same image under another tag.
package images

import (
	"errors"
	"strings"
)

// ErrForeign reports an image on another registry than the deployment's: the dashboard's
// credentials do not open it.
var ErrForeign = errors.New("image on another registry")

// Repository composes the full repository, without a tag.
//
// Empty when the project is: better to show nothing than a truncated name that
// would look like a pullable reference without being one.
func Repository(project, name string) string {
	if project == "" {
		return ""
	}
	return project + "/" + name
}

// Reference composes the full reference of a published image.
func Reference(project, name, tag string) string {
	repository := Repository(project, name)
	if repository == "" || tag == "" {
		return ""
	}
	return repository + ":" + tag
}

// Parsed is a reference read back.
type Parsed struct {
	// Registry is what comes before the name, `reg.example.fr/team`; empty for a bare name.
	Registry string
	// Name is the last segment of the repository, `portal`.
	Name string
	// Tag is empty when the reference carries none.
	Tag string
	// Digest is `sha256:…`, empty when the reference carries none.
	Digest string
}

// Parse reads a reference `registry/name:tag`, `registry/name@sha256:…` or both.
//
// A colon counts as a tag only in the last segment: `localhost:5000/portal` names a port.
func Parse(reference string) Parsed {
	var parsed Parsed
	rest := strings.TrimSpace(reference)
	if at := strings.Index(rest, "@"); at >= 0 {
		parsed.Digest, rest = rest[at+1:], rest[:at]
	}
	slash := strings.LastIndex(rest, "/")
	if colon := strings.LastIndex(rest, ":"); colon > slash {
		parsed.Tag, rest = rest[colon+1:], rest[:colon]
	}
	if slash >= 0 {
		parsed.Registry, parsed.Name = rest[:slash], rest[slash+1:]
	} else {
		parsed.Name = rest
	}
	return parsed
}
