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

// issueReference reconnaît `#1234`.
//
// Bornes explicites des deux côtés, et elles portent tout le sens de
// l'expression :
//
//   - `(^|[^\w#])` refuse `##1234` et `abc#1234`. Le premier est une ancre
//     Markdown, le second un identifiant qui contient un croisillon sans le
//     signifier ;
//   - `\b` en fin refuse `#1234abc`, qui n'est pas un numéro.
//
// Sans elles, un lien Markdown `[#12](…)` ou une empreinte `sha#1234` auraient
// rattaché des demandes qui n'ont rien à voir à une release — et une note de
// release fausse est pire qu'une note vide : elle se lit comme une liste
// vérifiée.
var issueReference = regexp.MustCompile(`(^|[^\w#])#(\d+)\b`)

// maxIssueNumber borne ce qu'on accepte comme numéro de demande.
//
// Un `#123456789012345` dans un texte est tout sauf une demande, et le convertir
// déborderait ou produirait un identifiant absurde qu'on irait chercher en base.
const maxIssueNumber = 1 << 31

// ExtractIssueIDs rend les numéros de demande cités dans un texte.
//
// Sans doublon et dans l'ordre d'apparition : une PR qui cite `#1234` dans son
// titre et dans trois commits désigne une seule demande, et l'ordre est celui
// que l'auteur a écrit.
func ExtractIssueRefs(texts ...string) []string {
	var found []string
	seen := map[string]bool{}

	for _, text := range texts {
		for _, match := range issueReference.FindAllStringSubmatch(text, -1) {
			// La borne reste numérique parce que le motif l'est : `#1234` est la
			// convention de cette équipe, et elle écarte les numéros absurdes —
			// un `#0` ou un identifiant à huit chiffres est un faux positif.
			//
			// Ce que rend la fonction, en revanche, est du **texte** : c'est ce
			// qui sera comparé aux identifiants du suivi, et tous ne numérotent
			// pas.
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
