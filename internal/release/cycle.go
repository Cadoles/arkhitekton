package release

import (
	"errors"
	"fmt"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/version"
)

/*
 * The release cycle: which kinds exist, and what each does.
 *
 * `pre` / `final` / `patch` were first classified as "generic" on the grounds that the vocabulary
 * is common. It was a mistake of method: the frequency of a word says nothing about its
 * generality, and what these kinds *do* had to be looked at.
 *
 * They do not name three words but three points in a space of independent traits — and the table
 * showed the defect: **`pre` and `patch` differ only in the tag's shape and the permission.**
 * Their behaviour is the same, building from code. Three names for two behaviours, and a `check
 * (kind in ('pre','final','patch'))` in the database that forbade declaring a fourth.
 *
 * A project on `alpha` / `beta` / `rc` / `ga` did not fit those three boxes. A project that
 * distinguishes nothing had to choose one arbitrarily.
 *
 * # What the separation gains
 *
 * The invariant — a final is a re-tag, never a rebuild — stops being a property of the **value**
 * `final` and becomes a property of the **mode** `retag`. It then holds for any kind that declares
 * it, whatever its name, and the code that holds it no longer cites any kind.
 */

// Mode is what a kind does to produce its image.
//
// A closed set, and that is what distinguishes it from kinds: these are the two
// primitives the dashboard can execute. Adding one takes new code, where adding a
// kind takes a line of configuration.
type Mode string

const (
	// ModeBuild builds an image from a git reference.
	ModeBuild Mode = "build"
	// ModeRetag reuses an existing release's image, bit for bit. Promoting or shipping
	// never rebuilds, on pain of shipping an image nobody tested.
	ModeRetag Mode = "retag"
)

// TagRule says what the kind requires of the tag's shape.
type TagRule string

const (
	// TagAny requires nothing. The only rule declarable on a scheme that cannot tell a
	// pre-release apart.
	TagAny TagRule = "any"
	// TagPrereleaseRequired requires a pre-release component.
	TagPrereleaseRequired TagRule = "prerelease-required"
	// TagPrereleaseForbidden l'interdit.
	TagPrereleaseForbidden TagRule = "prerelease-forbidden"
)

// KindSpec declares a kind of release.
type KindSpec struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Mode  Mode   `json:"mode"`
	// Tag bounds the shape of the tag. Empty means [TagAny].
	Tag TagRule `json:"tag,omitempty"`
	// Permission required to create a release of this kind. Empty means the ordinary
	// creation permission.
	Permission string `json:"permission,omitempty"`
	// Source names the kind a `retag` kind takes its image from. Empty: any earlier
	// release will do.
	Source string `json:"source,omitempty"`
}

// Cycle is a project's set of kinds, in the order the screen offers them.
type Cycle []KindSpec

// DefaultCycle returns the historical cycle — `pre`, `final`, `patch`.
//
// Exactly what the enumeration in the database described, trait for trait: that is
// what lets a project declared before this change carry on with nobody writing
// anything.
func DefaultCycle() Cycle {
	return Cycle{
		{
			ID: "pre", Label: "Pre-release", Mode: ModeBuild,
			Tag: TagPrereleaseRequired, Permission: "release:create",
		},
		{
			ID: "final", Label: "Finale", Mode: ModeRetag, Source: "pre",
			Tag: TagPrereleaseForbidden, Permission: "release:create:final",
		},
		{
			ID: "patch", Label: "Patch", Mode: ModeBuild,
			Tag: TagPrereleaseForbidden, Permission: "release:create:final",
		},
	}
}

// ErrUnknownKind reports a kind the project's cycle does not declare.
var ErrUnknownKind = errors.New("unknown release kind")

// Find returns the kind declared under this identifier.
//
// The empty identifier is told apart: it is a caller that chose nothing, not a
// misspelled kind, and saying so avoids a message quoting empty quotes. It
// happened — a form submitted its initial state rather than the kind it displayed
// as checked.
func (c Cycle) Find(id string) (KindSpec, error) {
	if strings.TrimSpace(id) == "" {
		return KindSpec{}, fmt.Errorf("%w: no kind was given (declared: %s)",
			ErrUnknownKind, strings.Join(c.ids(), ", "))
	}

	for _, spec := range c {
		if spec.ID == id {
			return spec, nil
		}
	}
	return KindSpec{}, fmt.Errorf("%w %q (declared: %s)",
		ErrUnknownKind, id, strings.Join(c.ids(), ", "))
}

// ids lists the declared kinds, to name them in an error message.
func (c Cycle) ids() []string {
	declared := make([]string, 0, len(c))
	for _, spec := range c {
		declared = append(declared, spec.ID)
	}
	return declared
}

// Validate refuses an inconsistent cycle, or one incompatible with the scheme.
//
// Checked at configuration time and not at first use: a `retag` kind whose source
// does not exist would only be discovered when shipping, which is the worst
// moment.
func (c Cycle) Validate(scheme version.Scheme) error {
	if len(c) == 0 {
		return errors.New("a project must declare at least one release kind")
	}

	// Can the scheme tell a pre-release apart? The question is put to it once, on a
	// canonical tag, rather than inferred from its name.
	probe, err := scheme.Parse("1.0.0")
	knowsPrerelease := err == nil && probe.KnowsPrerelease

	seen := map[string]bool{}
	var problems []error

	for _, spec := range c {
		if strings.TrimSpace(spec.ID) == "" {
			problems = append(problems, errors.New("a kind is declared without an identifier"))
			continue
		}
		if seen[spec.ID] {
			problems = append(problems, fmt.Errorf("kind %q is declared twice", spec.ID))
		}
		seen[spec.ID] = true

		switch spec.Mode {
		case ModeBuild, ModeRetag:
		default:
			problems = append(problems, fmt.Errorf(
				"kind %q: unknown mode %q (expected %s or %s)",
				spec.ID, spec.Mode, ModeBuild, ModeRetag))
		}

		switch spec.Tag {
		case "", TagAny:
		case TagPrereleaseRequired, TagPrereleaseForbidden:
			// The kind × scheme coupling, refused here rather than discovered. On a scheme that
			// does not distinguish pre-releases this constraint is meaningless: it could never
			// be checked, and a release created believing it held would go to an environment
			// that excludes it.
			if !knowsPrerelease {
				problems = append(problems, fmt.Errorf(
					"kind %q: rule %q assumes a scheme that distinguishes "+
						"pre-releases, and %q cannot answer",
					spec.ID, spec.Tag, scheme.ID()))
			}
		default:
			problems = append(problems, fmt.Errorf(
				"kind %q: unknown tag rule %q", spec.ID, spec.Tag))
		}
	}

	// Sources are checked second: they reference other kinds, and checking them inside
	// the loop would reject a source declared further down the list.
	for _, spec := range c {
		if spec.Mode != ModeRetag || spec.Source == "" {
			continue
		}
		if !seen[spec.Source] {
			problems = append(problems, fmt.Errorf(
				"kind %q: its source %q is not declared", spec.ID, spec.Source))
		}
	}

	return errors.Join(problems...)
}

// CheckTag checks that the tag's shape matches what the kind requires.
//
// The mistake it catches is banal and costly: creating `1.8.0-rc1` under a kind
// that forbids pre-releases, then finding production never picks it up — its
// version range excluding them, the deployer ignores it silently.
//
// A rule the scheme cannot check is accepted silently rather than refused:
// configuration has already forbidden it ([Cycle.Validate]), and failing here on a
// valid cycle would block a creation for a reason the user cannot fix.
func CheckTag(spec KindSpec, parsed version.Version) error {
	prerelease, known := parsed.Prereleased()
	if !known {
		return nil
	}

	switch spec.Tag {
	case TagPrereleaseRequired:
		if !prerelease {
			return fmt.Errorf(
				"tag %q has no pre-release component: a %q release is written, for example, %s-rc1",
				parsed.Raw, spec.ID, parsed.Base)
		}
	case TagPrereleaseForbidden:
		if prerelease {
			return fmt.Errorf(
				"tag %q carries a pre-release component %q: a %q release is written %s",
				parsed.Raw, parsed.Prerelease, spec.ID, parsed.Base)
		}
	}
	return nil
}
