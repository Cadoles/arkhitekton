package release

import (
	"strings"

	versionpkg "forge.cadoles.com/vcarroy/arkhitekton/internal/version"
)

/*
 * Matching a release tag with a version name from the tracker.
 *
 * The invariant "version name = release tag" does not hold as it is: the tracker names its
 * versions `v3.2.0`, the tag is written `3.2.0`. Hence a normalisation, applied to the matching
 * only. The names are kept as they are in the database: a version's identity is its identifier in
 * the tracker, and rewriting the name would lose what the user reads in it.
 *
 * # A single prefix, and no more setting
 *
 * The prefixes were configurable, and the MSE deployment declared `MSE ` besides `v`. That prefix
 * attached only closed versions with no release, and created with their `v` twins ambiguities the
 * adoption then had to settle. It was removed, and the setting with it: `v` comes from semantic
 * versioning and assumes nothing about a project.
 */

// semverPrefix is the prefix stripped before a version number.
const semverPrefix = "v"

// NormalizeVersionName returns the comparable form of a version name.
//
// The prefix is stripped only if the rest starts with a digit: that is what stops a
// version named `validation` from being reduced to `alidation`.
//
// Normalisation is not injective: `3.1.2` and `v3.1.2` both yield `3.1.2`, and a
// tracker may carry both. Any matching must therefore handle the ambiguous case and
// never assume a unique correspondence.
func NormalizeVersionName(name string) string {
	normalized := strings.TrimSpace(name)

	if len(normalized) > len(semverPrefix) &&
		strings.EqualFold(normalized[:len(semverPrefix)], semverPrefix) {
		if rest := normalized[len(semverPrefix):]; startsWithDigit(rest) {
			return rest
		}
	}
	return normalized
}

func startsWithDigit(value string) bool {
	return value != "" && value[0] >= '0' && value[0] <= '9'
}

// SameVersion says whether a tag and a tracker version name mean the same version.
//
// The comparison is on the base version: a pre-release `3.2.0-rc1` attaches to
// version `3.2.0`, which is what links a release candidate to its functional scope.
func SameVersion(version versionpkg.Version, trackedName string) bool {
	return NormalizeVersionName(version.Base) == NormalizeVersionName(trackedName)
}
