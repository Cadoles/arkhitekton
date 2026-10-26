package release

import (
	"regexp"
	"strconv"
)

/*
 * Matching a Redmine request with a pull request.
 *
 * The convention is the team's: the request number appears in the PR's title or in one of its
 * commit messages, in the form `#1234`. Nothing formalises it on the forge's side — no field, no
 * label — and it must therefore be read from text.
 *
 * This file does only that: extract numbers from a text. Deciding what to do with them — which
 * request to keep, in which category — belongs to the caller, which alone knows the state of the
 * requests.
 */

// issueReference recognises `#1234`.
//
// Explicit bounds on both sides, and they carry the whole meaning:
//
//   - `(^|[^\w#])` refuses `##1234` and `abc#1234`. The first is a Markdown anchor,
//     the second an identifier that contains a hash without meaning one;
//   - the trailing `\b` refuses `#1234abc`, which is not a number.
//
// Without them, a Markdown link `[#12](…)` or a fingerprint `sha#1234` would have
// attached unrelated issues to a release — and a false release note is worse than an
// empty one: it reads like a checked list.
var issueReference = regexp.MustCompile(`(^|[^\w#])#(\d+)\b`)

// maxIssueNumber bounds what is accepted as an issue number.
//
// A `#123456789012345` in a text is anything but an issue, and converting it would
// overflow or produce an absurd identifier that would be looked up in the database.
const maxIssueNumber = 1 << 31

// ExtractIssueIDs returns the issue numbers cited in a text.
//
// Without duplicates and in order of appearance: a pull request citing `#1234` in
// its title and in three commits names one issue, and the order is the one its
// author wrote.
func ExtractIssueRefs(texts ...string) []string {
	var found []string
	seen := map[string]bool{}

	for _, text := range texts {
		for _, match := range issueReference.FindAllStringSubmatch(text, -1) {
			// The bound stays numeric because the pattern is: `#1234` is this team's
			// convention, and it rules out absurd numbers — a `#0` or an eight-digit identifier
			// is a false positive.
			//
			// What the function returns, however, is text: it will be compared with the
			// tracker's identifiers, and not every tracker numbers.
			number, err := strconv.ParseInt(match[2], 10, 64)
			if err != nil || number <= 0 || number >= maxIssueNumber {
				continue
			}
			reference := match[2]
			if seen[reference] {
				continue
			}
			seen[reference] = true
			found = append(found, reference)
		}
	}
	return found
}
