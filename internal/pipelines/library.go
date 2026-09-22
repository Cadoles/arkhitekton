// Package pipelines carries the pipeline library the dashboard installs itself, and the mechanism
// that puts it in place.
//
// # Two tiers, one reason
//
// The **core** — build, re-tag — lives in the dashboard's `deploy/` and arrives with it, like the
// migration Job. It assumes no stack, and its version follows the binary's.
//
// The **reference** implementations assume a stack, and that is why they cannot live there: a Go
// project has no use for PHP Tasks. They are therefore installed **on demand**, when a project
// declares its own, which is exactly the criterion: the desired state changes when someone acts
// in the interface.
//
// # What this package is not
//
// Neither a package manager nor an extension mechanism. The libraries are **embedded in the
// binary**: nothing is downloaded, nothing is resolved at run time, and a library's version is
// that of the dashboard installing it. The day a project needs something else, it **forks** — the
// normative thing is the contract, not this implementation.
//
// # What the dashboard owns, and what it refuses
//
// It installs only resources it **names**, marked `managed-by`, and **never adopts** what already
// exists. It is the same rule as for the deploy-key secrets, and it has a happy consequence here:
// on a cluster where `mse-tekton` has already installed its pipelines, the dashboard observes and
// withdraws, instead of overwriting definitions someone else maintains.
//
// This mechanism therefore serves **autonomy**: an instance that needed no pre-existing pipeline
// repository.
package pipelines

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// library carries the reference libraries, one per stack.
//
//go:embed library
var library embed.FS

// Resource is a Tekton object to install, as it is written in the library.
type Resource struct {
	// Name is the object's name, read from its manifest. Serves to say what was installed, and what
	// was refused.
	Name string
	// Kind is `Pipeline` or `Task`.
	Kind string
	// Manifest is the YAML as it is. Not rewritten: the dashboard installs what the library declares,
	// and a manifest modified on the fly would be a definition nobody has reviewed.
	Manifest []byte
}

// Stacks lists the stacks a library exists for.
//
// One only today, and that is deliberate. `php` is extracted from what actually runs — the Tasks
// and Pipelines of `mse-tekton`, exercised by months of running. Writing a Node or Go library in
// advance would amount to guessing a test runner, output paths and a marker emission, that is, to
// producing code that compiles and verdicts that are wrong.
//
// They will be added against a real project. In the meantime, a project whose stack is not covered
// **writes its own** and has only a contract to honour: installable and forkable.
func Stacks() []string {
	entries, err := fs.ReadDir(library, "library")
	if err != nil {
		return nil
	}

	stacks := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			stacks = append(stacks, entry.Name())
		}
	}
	sort.Strings(stacks)
	return stacks
}

// ErrUnknownStack reports a stack no library exists for.
//
// Named rather than silent: a project declaring `node` today must learn it at declaration, and
// not find out that its pipelines do not exist at the first launch.
type ErrUnknownStack struct {
	Stack string
	Known []string
}

func (e ErrUnknownStack) Error() string {
	if len(e.Known) == 0 {
		return fmt.Sprintf("stack %q unknown: no reference library is embedded",
			e.Stack)
	}
	return fmt.Sprintf(
		"stack %q unknown (embedded: %s) — a project whose stack is not covered "+
			"writes its own pipelines and has only the contract to honour",
		e.Stack, strings.Join(e.Known, ", "))
}

// For returns the resources of a stack's library.
//
// An empty stack returns an empty list with no error: it is the case of a project bringing its own
// pipelines, and it is perfectly valid.
func For(stack string) ([]Resource, error) {
	stack = strings.TrimSpace(stack)
	if stack == "" {
		return nil, nil
	}

	entries, err := fs.ReadDir(library, path.Join("library", stack))
	if err != nil {
		return nil, ErrUnknownStack{Stack: stack, Known: Stacks()}
	}

	resources := make([]Resource, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		manifest, err := library.ReadFile(path.Join("library", stack, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading %s/%s: %w", stack, entry.Name(), err)
		}

		name, kind, err := identify(manifest)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", stack, entry.Name(), err)
		}
		resources = append(resources, Resource{Name: name, Kind: kind, Manifest: manifest})
	}
	return resources, nil
}

// identify reads a manifest's name and kind.
//
// A line-by-line read rather than a complete YAML decode: only two leading fields are sought, and
// decoding the whole object would require knowing Tekton's schema for nothing — the manifest is
// installed as it is.
func identify(manifest []byte) (name, kind string, err error) {
	for _, line := range strings.Split(string(manifest), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case kind == "" && strings.HasPrefix(trimmed, "kind:"):
			kind = strings.TrimSpace(strings.TrimPrefix(trimmed, "kind:"))
		case name == "" && strings.HasPrefix(line, "  name:"):
			// Indented by two spaces: it is `metadata.name`, and not the `name` of a parameter or a step,
			// which live deeper.
			name = strings.TrimSpace(strings.TrimPrefix(trimmed, "name:"))
		}
		if name != "" && kind != "" {
			break
		}
	}

	if name == "" || kind == "" {
		return "", "", fmt.Errorf("manifest with no name or no kind (read: %q/%q)", kind, name)
	}
	return name, kind, nil
}
