package release

import (
	"reflect"
	"testing"
)

// La convention telle qu'elle est écrite dans les titres de PR et les messages
// de commit.
func TestLesNumerosDeDemandeSontLusDansLeTexte(t *testing.T) {
	cases := map[string][]int64{
		"#1234 corrige la pagination":            {1234},
		"Corrige la pagination (#1234)":          {1234},
		"fix: pagination\n\nRefs #1234 et #5678": {1234, 5678},
		"aucun numéro ici":                       nil,
		// Le même numéro cité deux fois désigne une seule demande.
		"#42 puis encore #42": {42},
	}

	for text, expected := range cases {
		if got := ExtractIssueIDs(text); !reflect.DeepEqual(got, expected) {
			t.Errorf("ExtractIssueIDs(%q) = %v, attendu %v", text, got, expected)
		}
	}
}

// Ce que l'expression doit **refuser**, et c'est le cœur du sujet : une note de
// release fausse est pire qu'une note vide, parce qu'elle se lit comme une liste
// vérifiée.
func TestCeQuiRessembleAUnNumeroSansEnEtreUnEstRefuse(t *testing.T) {
	refuses := []string{
		// Ancre Markdown, pas une demande.
		"## 1234 Titre de section",
		"##1234",
		// Un croisillon collé à un mot : identifiant d'autre chose.
		"commit abc#1234",
		"branche feature#99",
		// Un numéro suivi de lettres n'est pas un numéro.
		"#1234abc",
		// Zéro n'est pas un identifiant Redmine.
		"#0",
	}

	for _, text := range refuses {
		if got := ExtractIssueIDs(text); len(got) != 0 {
			t.Errorf("ExtractIssueIDs(%q) = %v, attendu aucun", text, got)
		}
	}
}

// Un nombre démesuré n'est pas une demande : le convertir déborderait, et le
// chercher en base ne rendrait rien tout en donnant l'illusion d'un lien.
func TestUnNumeroDemesureEstIgnore(t *testing.T) {
	if got := ExtractIssueIDs("#123456789012345"); len(got) != 0 {
		t.Errorf("ExtractIssueIDs = %v, attendu aucun", got)
	}
}

// Titre et commits sont lus ensemble : la convention autorise l'un ou l'autre,
// et une PR qui cite sa demande dans un seul de ses commits doit compter.
func TestLeTitreEtLesCommitsSontLusEnsemble(t *testing.T) {
	got := ExtractIssueIDs(
		"Corrige la pagination",
		"wip",
		"fix(pagination): borne l'offset\n\nRefs #1234",
	)
	if !reflect.DeepEqual(got, []int64{1234}) {
		t.Errorf("ExtractIssueIDs = %v, attendu [1234]", got)
	}
}

// L'ordre est celui de l'écriture : c'est celui dans lequel l'auteur a listé ses
// demandes, et le trier alphabétiquement ou numériquement perdrait cette
// intention sans rien apporter.
func TestLOrdreDApparitionEstConserve(t *testing.T) {
	got := ExtractIssueIDs("Refs #99, #12, #55")
	if !reflect.DeepEqual(got, []int64{99, 12, 55}) {
		t.Errorf("ExtractIssueIDs = %v, attendu [99 12 55]", got)
	}
}
