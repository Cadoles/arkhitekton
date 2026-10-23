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
// Neither a package manager nor an extension mechanism. The definitions come from the plugins
// compiled into the binary, gathered by stack when the dashboard is assembled: nothing is
// downloaded, nothing is resolved at run time, and a library's version is that of the dashboard
// installing it.
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
	"fmt"
	"sort"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

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
	// Plugin is the plugin that ships it, set on the object as a label: what lets the dashboard
	// withdraw a plugin's definitions once it is uninstalled.
	Plugin string
}

// Library is the set of reference libraries, one per stack.
type Library struct {
	stacks map[string][]Resource
}

// NewLibrary gathers the plugins' manifests by stack.
func NewLibrary(stacks map[string][]capability.Manifest) *Library {
	library := &Library{stacks: make(map[string][]Resource, len(stacks))}
	for stack, manifests := range stacks {
		for _, manifest := range manifests {
			library.stacks[stack] = append(library.stacks[stack],
				Resource{Name: manifest.Name, Kind: manifest.Kind, Manifest: manifest.YAML})
		}
	}
	return library
}

// Stacks lists the stacks a library exists for.
func (l *Library) Stacks() []string {
	if l == nil {
		return nil
	}
	stacks := make([]string, 0, len(l.stacks))
	for stack := range l.stacks {
		stacks = append(stacks, stack)
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
func (l *Library) For(stack string) ([]Resource, error) {
	stack = strings.TrimSpace(stack)
	if stack == "" {
		return nil, nil
	}
	if l != nil {
		if resources, found := l.stacks[stack]; found {
			return resources, nil
		}
	}
	return nil, ErrUnknownStack{Stack: stack, Known: l.Stacks()}
}
