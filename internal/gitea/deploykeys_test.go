package gitea

import "testing"

/*
 * La comparaison de deux clés publiques.
 *
 * Elle décide, seule, si les constructions d'un projet partent ou non. Un faux
 * négatif enverrait recoller une clé déjà posée ; un faux positif ferait croire
 * qu'un dépôt s'ouvre alors que le clone échouera.
 */

const cle = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBDoQT4jgoSvFdwjmhS3GhAn9/48tIieJhlrx1mpZm93"

// Le commentaire ne compte pas : il est libre, et la forge le réécrit souvent au
// moment du collage — Gitea y met le titre de la clé. Comparer les chaînes
// entières répondrait « absente » sur une clé pourtant en place.
func TestLeCommentaireNeDecidePasDeLIdentiteDUneCle(t *testing.T) {
	for _, variante := range []string{
		cle,
		cle + " mse-dashboard",
		cle + " un tout autre commentaire",
		"  " + cle + " avec des espaces  ",
	} {
		if !SameKey(variante, cle+" mse-dashboard-project-mse-git") {
			t.Errorf("variante %q jugée différente", variante)
		}
	}
}

// Deux clés distinctes le restent, même sous le même commentaire : c'est le cas
// d'une rotation où l'ancienne n'a pas été remplacée dans le dépôt.
func TestDeuxClesDistinctesLeRestent(t *testing.T) {
	autre := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAutreMatiereQuiNestPasLaMeme"

	if SameKey(cle+" mse-dashboard", autre+" mse-dashboard") {
		t.Error("deux matières différentes jugées identiques")
	}
	// Le type compte aussi : une RSA et une ed25519 ne sont pas la même clé,
	// quand bien même leur matière commencerait pareil.
	if SameKey("ssh-rsa AAAAB3", "ssh-ed25519 AAAAB3") {
		t.Error("deux types différents jugés identiques")
	}
}

// Une entrée sans matière ne correspond à rien, **pas même à elle-même**.
//
// C'est la garde qui compte ici : un projet dont la clé n'a pas encore été posée
// porte une chaîne vide, et sans cette règle il se croirait reconnu dans un dépôt
// qui n'en déclare aucune. Il partirait alors construire avec une clé
// inexistante, et le clone échouerait loin de la cause.
//
// La comparaison ne cherche pas à valider la syntaxe d'une clé au-delà : ce n'est
// pas son rôle, et deux chaînes bien formées mais fantaisistes ont le droit de se
// ressembler.
func TestUneEntreeSansMatiereNeCorrespondARien(t *testing.T) {
	for _, sansMatiere := range []string{"", "   ", "ssh-ed25519"} {
		if SameKey(sansMatiere, cle) {
			t.Errorf("entrée %q jugée identique à une clé valide", sansMatiere)
		}
		if SameKey(sansMatiere, sansMatiere) {
			t.Errorf("entrée %q jugée identique à elle-même", sansMatiere)
		}
	}
}
