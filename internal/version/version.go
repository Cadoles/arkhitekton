// Package version carries the version scheme, which the rest asks questions of
// rather than reading fields from.
//
// SemVer was not a building block of this project, it was an ambient assumption: the
// shape MAJOR.MINOR.PATCH[-prerelease] was coded into internal/release, and
// everything relying on it — range selection, deducing the next candidate, the join
// with the tracker — took it for granted without ever naming it.
//
// It is not universal. The immediate neighbourhood gives the example: a GitOps
// repository's tags read like disguised calendar versions. Other projects number
// their builds, or order nothing at all.
//
// Six questions, and each may go unanswered. That is the design point: an opaque
// scheme must stay usable. A project whose tags read `build-4711` loses the next
// version suggestion and range selection, but keeps everything else — building,
// testing, grading, deploying by explicit tag. The tool must say so rather than
// compute something arbitrary.
//
//	Question                        SemVer         Opaque
//	-----------------------------   ------------   --------------
//	Is this tag well formed?        yes            everything is
//	What is its version line?       `3.2.0`        unknown
//	Is it a pre-release?            yes/no         unknown
//	What is the next one?           computable     unknown
//	How to attach a marker to it?   metadata       impossible
//	How to order two versions?      precedence     unknown
//
// This is not an outside source: nothing is read elsewhere, no failure is possible.
// It is a convention of the project, declared in its configuration, which is why
// this package lives apart from internal/provider.
package version

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrUnsupported says a scheme cannot answer this question.
//
// Not a failure, and that is the nuance: the opaque scheme returns this error for
// half the questions, and a project using it works. The caller must say it does not
// know, never guess — an invented next version or an arbitrary order would produce
// false decisions nothing would flag.
var ErrUnsupported = errors.New("the version scheme does not answer this question")

// Version is a tag decomposed as far as the scheme can.
type Version struct {
	// Raw is the tag as it was written. Always set: it is the only thing every scheme
	// knows.
	Raw string
	// Base is the version line — `1.8.0` for `1.8.0-rc1`. Empty when the scheme does not
	// know it.
	Base string
	// Prerelease is the pre-release component. Empty when there is none — or when the
	// scheme cannot tell, hence [Version.Prereleased].
	Prerelease string
	// KnowsPrerelease says whether "is this a pre-release" means anything for this
	// scheme. When false, an empty Prerelease does not mean "final release": it means
	// "unknown".
	KnowsPrerelease bool
}

// Prereleased says whether the tag is a pre-release, and whether the question means
// anything.
//
// Two returns rather than a boolean: on an opaque scheme, returning false alone
// would assert that a release is final when nothing says so. That is exactly the
// kind of assertion that promotes to production a tag nobody approved.
func (v Version) Prereleased() (bool, bool) {
	if !v.KnowsPrerelease {
		return false, false
	}
	return v.Prerelease != "", true
}

// Scheme answers for the shape of a project's tags.
type Scheme interface {
	// ID names the scheme, as it is declared in configuration.
	ID() string
	// Parse decomposes a tag, or refuses one that does not have the expected shape.
	Parse(tag string) (Version, error)
	// Compare orders two tags — negative, zero, positive — or returns [ErrUnsupported].
	// Without it the interface shows creation order rather than version order, and says
	// so.
	Compare(a, b string) (int, error)

	// NextPrerelease proposes the next pre-release of a version line, given those that
	// already exist, or returns [ErrUnsupported].
	//
	// The interface used to answer it itself, hard-coding `-rc<N>`: one project's
	// convention imposed on every project whose pre-releases read otherwise. A wrong
	// suggestion is not dramatic — the field stays editable — but it teaches a convention
	// to whoever discovers the tool, and that is how an ambient assumption reinstalls
	// itself.
	NextPrerelease(base string, existing []string) (string, error)
}

// Schemes lists the declarable schemes, in the order the screen offers them.
//
// Calendar versioning is not among them: no declared project uses one, and writing
// it in advance would mean guessing a convention. It will be added with the first
// project that has one, against a real case.
func Schemes() []Scheme { return []Scheme{SemVer{}, Opaque{}} }

// For returns the declared scheme, or an error naming those that exist.
//
// Empty means SemVer: it is what every declared project uses, and requiring an
// explicit declaration would have invalidated rows nobody wrote.
func For(id string) (Scheme, error) {
	if strings.TrimSpace(id) == "" {
		return SemVer{}, nil
	}
	for _, scheme := range Schemes() {
		if scheme.ID() == id {
			return scheme, nil
		}
	}

	known := make([]string, 0, len(Schemes()))
	for _, scheme := range Schemes() {
		known = append(known, scheme.ID())
	}
	return nil, fmt.Errorf("unknown version scheme %q (known: %s)",
		id, strings.Join(known, ", "))
}

// semverPattern covers the shape kept: MAJOR.MINOR.PATCH with an optional
// pre-release. Build metadata is deliberately absent — the separator kept for
// promotion is the underscore, since OCI tag grammar excludes `+`.
var semverPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)

// SemVer is the scheme that answers everything.
type SemVer struct{}

var _ Scheme = SemVer{}

func (SemVer) ID() string { return "semver" }

// Parse refuses rather than tolerates: a malformed tag would break the join with the
// tracker and range selection, and it is better said at creation than discovered by
// noticing an environment never moves.
func (SemVer) Parse(tag string) (Version, error) {
	matches := semverPattern.FindStringSubmatch(strings.TrimSpace(tag))
	if matches == nil {
		return Version{}, fmt.Errorf(
			"tag %q invalid: expected MAJOR.MINOR.PATCH[-prerelease]", tag)
	}

	return Version{
		Raw:             matches[0],
		Base:            fmt.Sprintf("%s.%s.%s", matches[1], matches[2], matches[3]),
		Prerelease:      matches[4],
		KnowsPrerelease: true,
	}, nil
}

// Compare orders by SemVer precedence.
//
// The rule that counts, and that a lexical sort misses: a pre-release always precedes
// its final version — `1.8.0-rc1` comes before `1.8.0` — whereas `-` sorts after the
// empty string the other way round.
func (s SemVer) Compare(a, b string) (int, error) {
	first, err := s.Parse(a)
	if err != nil {
		return 0, err
	}
	second, err := s.Parse(b)
	if err != nil {
		return 0, err
	}

	if order := compareNumericBase(first.Base, second.Base); order != 0 {
		return order, nil
	}

	switch {
	case first.Prerelease == second.Prerelease:
		return 0, nil
	case first.Prerelease == "":
		// A final version follows all its pre-releases.
		return 1, nil
	case second.Prerelease == "":
		return -1, nil
	}
	return strings.Compare(first.Prerelease, second.Prerelease), nil
}

// compareNumericBase compares two `X.Y.Z` lines component by component.
//
// Numerically and not lexically: `1.10.0` follows `1.9.0`, where string comparison
// puts it before.
func compareNumericBase(a, b string) int {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for i := range left {
		if i >= len(right) {
			return 1
		}
		if order := compareNumbers(left[i], right[i]); order != 0 {
			return order
		}
	}
	if len(right) > len(left) {
		return -1
	}
	return 0
}

func compareNumbers(a, b string) int {
	// The components come from the pattern: they carry digits only, and comparing their
	// length first is enough to order them numerically without conversion — hence with
	// no possible overflow on an absurd number.
	switch {
	case len(a) != len(b):
		if len(a) > len(b) {
			return 1
		}
		return -1
	}
	return strings.Compare(a, b)
}

// NextPrerelease proposes `<base>-rc<N+1>`.
//
// The `rc` convention is the project's and not SemVer's, which imposes nothing on
// the pre-release component. It lives here rather than in the interface because the
// scheme is what knows where that component goes; the day a project wants another,
// it will be one more scheme, not a form to fix.
func (s SemVer) NextPrerelease(base string, existing []string) (string, error) {
	if _, err := s.Parse(base); err != nil {
		return "", err
	}

	highest := 0
	for _, tag := range existing {
		parsed, err := s.Parse(tag)
		if err != nil || parsed.Base != base {
			continue
		}
		if number, ok := rcNumber(parsed.Prerelease); ok && number > highest {
			highest = number
		}
	}
	return fmt.Sprintf("%s-rc%d", base, highest+1), nil
}

// rcNumber reads the rank of an `rcN` pre-release.
//
// Other forms are ignored rather than refused: a project may carry `1.8.0-beta1` in
// its history, and taking offence would fail a suggestion for a release that
// legitimately exists.
func rcNumber(prerelease string) (int, bool) {
	digits, found := strings.CutPrefix(prerelease, "rc")
	if !found || digits == "" {
		return 0, false
	}
	number := 0
	for _, character := range digits {
		if character < '0' || character > '9' {
			return 0, false
		}
		number = number*10 + int(character-'0')
	}
	return number, true
}

// Opaque is the scheme that asserts nothing.
//
// It must stay usable, and it is what proves the contract: a scheme written against
// the SemVer implementation alone would have taken its shape without anyone
// noticing. Faced with ignorance, the contract had to make every question fallible
// rather than return zero values — an empty Base and a false pre-release would have
// been indistinguishable from affirmative answers.
type Opaque struct{}

var _ Scheme = Opaque{}

func (Opaque) ID() string { return "opaque" }

// Parse accepts any non-empty tag. With no declared convention, nothing allows
// saying a tag is malformed, and refusing it would impose a rule the project does
// not have.
func (Opaque) Parse(tag string) (Version, error) {
	trimmed := strings.TrimSpace(tag)
	if trimmed == "" {
		return Version{}, errors.New("empty tag")
	}
	// Base stays empty and KnowsPrerelease false: the tag does not decompose.
	return Version{Raw: trimmed}, nil
}

// NextPrerelease refuses: with no grammar, "the next one" does not exist. Proposing
// `-rc1` would teach a convention the project did not choose.
func (Opaque) NextPrerelease(string, []string) (string, error) {
	return "", fmt.Errorf("%w: proposing the next version", ErrUnsupported)
}

// Compare refuses: two opaque tags have no known order. A lexical sort would be an
// invented order, and the interface would display it as though it were the right
// one.
func (Opaque) Compare(string, string) (int, error) {
	return 0, fmt.Errorf("%w: ordering two versions", ErrUnsupported)
}
