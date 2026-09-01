package version

import (
	"errors"
	"strings"
	"testing"
)

// La forme retenue, et ce qu'elle refuse. Un tag hors forme casserait la jointure
// avec le suivi et la sélection par plage : mieux vaut le dire à la création que
// le découvrir en constatant qu'un environnement ne bouge pas.
func TestLeSemVerDecomposeEtRefuse(t *testing.T) {
	scheme := SemVer{}

	version, err := scheme.Parse("1.8.0-rc1")
	if err != nil {
		t.Fatalf("tag valide refusé: %v", err)
	}
	if version.Base != "1.8.0" || version.Prerelease != "rc1" {
		t.Errorf("décomposition: %+v", version)
	}
	prerelease, known := version.Prereleased()
	if !known || !prerelease {
		t.Errorf("pré-release: %v/%v", prerelease, known)
	}

	for _, tag := range []string{"", "v1.8.0", "1.8", "1.8.0+build", "build-4711"} {
		if _, err := scheme.Parse(tag); err == nil {
			t.Errorf("tag %q accepté", tag)
		}
	}
}

// Une finale répond « non, et j'en suis sûr ». C'est ce que le second retour
// distingue d'un schéma qui ne sait pas.
func TestUneFinaleSemVerAffirmeNetrePasUnePrerelease(t *testing.T) {
	version, _ := SemVer{}.Parse("1.8.0")

	prerelease, known := version.Prereleased()
	if prerelease || !known {
		t.Errorf("pré-release: %v/%v — une finale SemVer est une réponse ferme",
			prerelease, known)
	}
}

// L'ordre SemVer, et les deux pièges qu'un tri lexical rate.
func TestLOrdreSemVerNestPasLexical(t *testing.T) {
	scheme := SemVer{}

	// `1.10.0` suit `1.9.0` : comparé comme des chaînes, il le précéderait.
	if order, err := scheme.Compare("1.10.0", "1.9.0"); err != nil || order <= 0 {
		t.Errorf("1.10.0 vs 1.9.0 = %d (%v) — la comparaison doit être numérique", order, err)
	}

	// Une pré-release précède sa finale : `-` trie pourtant après la chaîne vide.
	if order, err := scheme.Compare("1.8.0-rc1", "1.8.0"); err != nil || order >= 0 {
		t.Errorf("1.8.0-rc1 vs 1.8.0 = %d (%v) — une RC précède sa finale", order, err)
	}

	if order, err := scheme.Compare("1.8.0", "1.8.0"); err != nil || order != 0 {
		t.Errorf("égalité = %d (%v)", order, err)
	}
	if order, err := scheme.Compare("1.8.0-rc1", "1.8.0-rc2"); err != nil || order >= 0 {
		t.Errorf("rc1 vs rc2 = %d (%v)", order, err)
	}
}

// Le marqueur se pose en underscore : la grammaire des tags OCI exclut le `+`
// que le SemVer réserve à la métadonnée de build (B7).
func TestLeMarqueurSemVerSePoseEnUnderscore(t *testing.T) {
	got, err := SemVer{}.Mark("1.8.0-rc1", "testing")
	if err != nil {
		t.Fatalf("marquage: %v", err)
	}
	if got != "1.8.0-rc1_testing" {
		t.Errorf("tag marqué = %q", got)
	}

	// Sans marqueur, le tag ressort inchangé plutôt que suffixé d'un underscore
	// orphelin, que le registre accepterait sans que rien ne le sélectionne.
	if got, _ := (SemVer{}).Mark("1.8.0", ""); got != "1.8.0" {
		t.Errorf("tag sans marqueur = %q", got)
	}
}

/*
 * Le schéma opaque, et c'est lui qui prouve le contrat.
 *
 * Un contrat écrit sur la seule réalisation SemVer en aurait épousé la forme sans
 * qu'on s'en aperçoive : chaque question aurait rendu une valeur, et l'absence de
 * réponse aurait été indiscernable d'une réponse négative.
 */

// Tout tag non vide est bien formé : sans convention déclarée, rien ne permet de
// dire le contraire, et le refuser imposerait une règle que le projet n'a pas.
func TestLeSchemaOpaqueAccepteToutTagNonVide(t *testing.T) {
	for _, tag := range []string{"build-4711", "2026.7.27-k8sunstable.1651", "v1.8.0", "n'importe quoi"} {
		version, err := Opaque{}.Parse(tag)
		if err != nil {
			t.Errorf("tag %q refusé: %v", tag, err)
			continue
		}
		if version.Raw == "" {
			t.Errorf("tag %q décomposé en rien", tag)
		}
	}

	if _, err := (Opaque{}).Parse("   "); err == nil {
		t.Error("un tag vide reste refusé")
	}
}

// Le piège que le second retour de `Prereleased` existe pour éviter : sur un
// schéma opaque, `false` seul affirmerait qu'une release est finale alors que
// rien ne le dit — et c'est ainsi qu'on promeut en production un tag que
// personne n'a validé.
func TestLeSchemaOpaqueNaffirmeJamaisQuUnTagEstFinal(t *testing.T) {
	version, err := Opaque{}.Parse("build-4711")
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}

	prerelease, known := version.Prereleased()
	if known {
		t.Error("le schéma opaque ne peut pas répondre à cette question")
	}
	if prerelease {
		t.Error("et il ne doit surtout pas répondre « oui »")
	}
	if version.Base != "" {
		t.Errorf("ligne de version %q — elle est inconnue", version.Base)
	}
}

// Les deux questions auxquelles il refuse de répondre. Une réponse inventée —
// un ordre lexical, un marqueur collé au hasard — serait affichée comme si elle
// était la bonne.
func TestLeSchemaOpaqueRefuseCeQuIlIgnore(t *testing.T) {
	if _, err := (Opaque{}).Mark("build-4711", "testing"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("marquage: %v, attendu ErrUnsupported", err)
	}
	if _, err := (Opaque{}).Compare("build-4711", "build-4712"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("comparaison: %v, attendu ErrUnsupported", err)
	}
}

// La déclaration se résout, et l'inconnue nomme ce qui existe plutôt que de
// laisser chercher dans le code.
func TestLaDeclarationDeSchemaSeResout(t *testing.T) {
	// Vide vaut SemVer : c'est ce que tous les projets déclarés emploient, et
	// exiger une déclaration explicite aurait invalidé des lignes existantes.
	scheme, err := For("")
	if err != nil || scheme.ID() != "semver" {
		t.Errorf("schéma par défaut = %v (%v)", scheme, err)
	}

	if scheme, err := For("opaque"); err != nil || scheme.ID() != "opaque" {
		t.Errorf("schéma opaque = %v (%v)", scheme, err)
	}

	_, err = For("calendaire")
	if err == nil {
		t.Fatal("un schéma inconnu doit être refusé")
	}
	for _, name := range []string{"semver", "opaque"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("message %q — il doit nommer les schémas connus", err)
		}
	}
}

// La sixième question du tableau, celle qui manquait.
//
// L'interface y répondait elle-même en codant `-rc<N>` : la convention du MSE,
// imposée à tout projet dont les pré-releases s'écrivent autrement. Une
// suggestion fausse n'est pas dramatique — le champ reste modifiable — mais elle
// **enseigne** une convention, et c'est ainsi qu'une hypothèse ambiante se
// réinstalle.
func TestLaPrereleaseSuivanteSeDeduitDeCellesQuiExistent(t *testing.T) {
	scheme := SemVer{}

	// Aucune : la première.
	got, err := scheme.NextPrerelease("1.8.0", nil)
	if err != nil || got != "1.8.0-rc1" {
		t.Errorf("première = %q (%v)", got, err)
	}

	// La suivante se déduit du rang le plus haut, et non du nombre de releases :
	// proposer `-rc3` parce qu'il y en a trois échouerait si `rc5` existe.
	existing := []string{"1.8.0-rc1", "1.8.0-rc5", "1.8.0-rc2"}
	if got, err := scheme.NextPrerelease("1.8.0", existing); err != nil || got != "1.8.0-rc6" {
		t.Errorf("suivante = %q (%v)", got, err)
	}

	// Les autres lignes ne comptent pas : `1.9.0-rc4` ne dit rien de `1.8.0`.
	if got, _ := scheme.NextPrerelease("1.8.0", []string{"1.9.0-rc4"}); got != "1.8.0-rc1" {
		t.Errorf("ligne voisine prise en compte: %q", got)
	}

	// Une pré-release d'une autre forme est ignorée plutôt que refusée : un
	// historique peut porter `1.8.0-beta1` légitimement.
	if got, _ := scheme.NextPrerelease("1.8.0", []string{"1.8.0-beta1"}); got != "1.8.0-rc1" {
		t.Errorf("forme étrangère: %q", got)
	}
}

// Le schéma opaque refuse : sans grammaire, la « suivante » n'existe pas, et
// proposer `-rc1` enseignerait une convention que le projet n'a pas choisie.
func TestLeSchemaOpaqueNeProposeAucuneSuivante(t *testing.T) {
	if _, err := (Opaque{}).NextPrerelease("build-4711", nil); !errors.Is(err, ErrUnsupported) {
		t.Errorf("suivante: %v, attendu ErrUnsupported", err)
	}
}
