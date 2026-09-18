package release

import (
	"regexp"
	"strconv"
)

/*
 * Rapprochement entre une demande Redmine et une pull request.
 *
 * La convention est celle de l'équipe : le numéro de demande apparaît dans le
 * titre de la PR ou dans l'un de ses messages de commit, sous la forme `#1234`.
 * Rien ne la formalise côté forge — pas de champ, pas d'étiquette — et c'est
 * donc du texte qu'il faut la lire.
 *
 * Ce fichier ne fait que cela : extraire des numéros d'un texte. Décider ce
 * qu'on en fait — quelle demande retenir, dans quelle catégorie — appartient à
 * l'appelant, qui seul connaît l'état des demandes.
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
