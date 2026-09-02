package secrets

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// Deux clés distinctes, produites comme le déploiement les produit.
const (
	keyA = "sN0Xk1t2Qv8yZ3aB4cD5eF6gH7iJ8kL9mN0oP1qR2sQ="
	keyB = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
)

func mustCipher(t *testing.T, current string, retired ...string) *Cipher {
	t.Helper()
	c, err := New(current, retired...)
	if err != nil {
		t.Fatalf("construction: %v", err)
	}
	return c
}

// L'aller-retour, et ce que l'enveloppe ne montre pas.
func TestUneValeurScelleeSeRouvreEtNeSeLitPas(t *testing.T) {
	c := mustCipher(t, keyA)

	sealed, err := c.Seal("clé-redmine-de-production")
	if err != nil {
		t.Fatalf("scellement: %v", err)
	}
	if strings.Contains(sealed, "redmine") {
		t.Fatalf("le clair transparaît dans %q", sealed)
	}

	opened, err := c.Open(sealed)
	if err != nil {
		t.Fatalf("ouverture: %v", err)
	}
	if opened != "clé-redmine-de-production" {
		t.Errorf("valeur ouverte = %q", opened)
	}
}

// Deux scellements de la même valeur diffèrent. Sans cela, comparer deux lignes
// de la base dirait qu'elles portent le même secret — une fuite en soi.
func TestDeuxScellementsDeLaMemeValeurDifferent(t *testing.T) {
	c := mustCipher(t, keyA)

	first, _ := c.Seal("identique")
	second, _ := c.Seal("identique")
	if first == second {
		t.Error("deux enveloppes identiques : le nonce ne varie pas")
	}
}

// Le vide reste vide : c'est l'effacement d'un champ, et une enveloppe le ferait
// passer pour renseigné.
func TestLeVideNeSeScellePas(t *testing.T) {
	c := mustCipher(t, keyA)

	if sealed, err := c.Seal(""); err != nil || sealed != "" {
		t.Errorf("scellement du vide = %q (%v)", sealed, err)
	}
	if opened, err := c.Open(""); err != nil || opened != "" {
		t.Errorf("ouverture du vide = %q (%v)", opened, err)
	}
}

// Sans clé, le chiffreur refuse d'écrire plutôt que de laisser passer du clair.
func TestSansCleLeChiffreurRefuseDecrire(t *testing.T) {
	var absent *Cipher

	if absent.Configured() {
		t.Error("un chiffreur absent se dit configuré")
	}
	if _, err := absent.Seal("mot de passe"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("scellement: %v, attendu ErrNotConfigured", err)
	}
	if _, err := absent.Open("v1:abcd:xxxx"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("ouverture: %v, attendu ErrNotConfigured", err)
	}
}

// Une clé mal produite se découvre au démarrage, pas à la première écriture.
func TestUneCleMalFormeeEstRefuseeALaConstruction(t *testing.T) {
	for _, key := range []string{
		"",
		"pas du base64 !",
		base64.StdEncoding.EncodeToString([]byte("trop court")),
	} {
		if _, err := New(key); err == nil {
			t.Errorf("clé %q acceptée", key)
		}
	}
}

/*
 * La rotation, et la raison d'être de l'empreinte.
 */

// L'ancienne clé continue d'ouvrir ce qu'elle avait scellé.
func TestUneCleRetireeOuvreEncoreCeQuElleAvaitScelle(t *testing.T) {
	ancien := mustCipher(t, keyB)
	sealed, err := ancien.Seal("secret d'avant la rotation")
	if err != nil {
		t.Fatalf("scellement: %v", err)
	}

	// Après rotation : `keyA` scelle, `keyB` ne fait plus qu'ouvrir.
	nouveau := mustCipher(t, keyA, keyB)
	opened, err := nouveau.Open(sealed)
	if err != nil {
		t.Fatalf("ouverture après rotation: %v", err)
	}
	if opened != "secret d'avant la rotation" {
		t.Errorf("valeur = %q", opened)
	}

	// Et le chiffreur sait dire que cette valeur reste à re-sceller — sans quoi
	// on ne saurait jamais quand la clé retirée peut quitter la configuration.
	if !nouveau.NeedsResealing(sealed) {
		t.Error("une valeur de l'ancienne clé devrait demander un re-scellement")
	}
	resealed, _ := nouveau.Seal("secret d'avant la rotation")
	if nouveau.NeedsResealing(resealed) {
		t.Error("une valeur de la clé courante ne demande rien")
	}
}

// Une rotation faite sans garder l'ancienne clé se dit, et nomme l'empreinte
// manquante : la retrouver parmi des clés archivées serait sinon à l'aveugle.
func TestUneCleAbsenteSeNommeParSonEmpreinte(t *testing.T) {
	sealed, _ := mustCipher(t, keyB).Seal("valeur orpheline")

	_, err := mustCipher(t, keyA).Open(sealed)
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("ouverture: %v, attendu ErrUnknownKey", err)
	}

	_, empreinte, _, _ := splitEnvelope(sealed)
	if !strings.Contains(err.Error(), empreinte) {
		t.Errorf("message %q — il doit nommer l'empreinte %s", err, empreinte)
	}
}

// Une enveloppe altérée est refusée, y compris quand seul son préfixe l'est :
// l'empreinte est authentifiée, on ne fait donc pas ouvrir une valeur par une
// clé qui n'est pas la sienne.
func TestUneEnveloppeAltereeEstRefusee(t *testing.T) {
	c := mustCipher(t, keyA, keyB)
	sealed, _ := c.Seal("valeur intègre")

	version, keyID, payload, ok := splitEnvelope(sealed)
	if !ok {
		t.Fatalf("enveloppe non décomposable: %q", sealed)
	}

	// Charge modifiée.
	altered := version + ":" + keyID + ":" + payload[:len(payload)-2] + "AA"
	if _, err := c.Open(altered); err == nil {
		t.Error("charge modifiée acceptée")
	}

	// Préfixe maquillé vers l'autre clé connue.
	_, autreID, _, _ := splitEnvelope(mustSeal(t, mustCipher(t, keyB), "x"))
	if _, err := c.Open(version + ":" + autreID + ":" + payload); err == nil {
		t.Error("empreinte maquillée acceptée")
	}

	for _, malformed := range []string{"sans-separateur", "v1:", "v1::charge", ":abcd:charge"} {
		if _, err := c.Open(malformed); err == nil {
			t.Errorf("enveloppe %q acceptée", malformed)
		}
	}

	// Version inconnue : le message dit celle que ce binaire lit, faute de quoi
	// une base écrite par une version future se diagnostiquerait à tâtons.
	if _, err := c.Open("v2:" + keyID + ":" + payload); err == nil ||
		!strings.Contains(err.Error(), envelopeVersion) {
		t.Errorf("version inconnue: %v", err)
	}
}

func mustSeal(t *testing.T, c *Cipher, plaintext string) string {
	t.Helper()
	sealed, err := c.Seal(plaintext)
	if err != nil {
		t.Fatalf("scellement: %v", err)
	}
	return sealed
}
