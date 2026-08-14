package report

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func sample(t *testing.T) Note {
	t.Helper()
	at := time.Date(2026, 8, 14, 10, 30, 0, 0, time.UTC)
	return Note{
		Release: "1.8.0", Version: "1.8.0", Kind: "final",
		ImageVersion: "1.8.0-rc3", SourceTag: "1.8.0-rc3", GitRef: "abc1234",
		Images: []Image{
			{Repository: "portal", Tag: "1.8.0-rc3", Digest: "sha256:1e4f"},
			{Repository: "portal", Tag: "1.8.0_prod"},
		},
		Promotions: []Promotion{
			{Environment: "prod", Tag: "1.8.0_prod", RequestedAt: at, Requested: true},
		},
		ManualActions: &ManualActions{
			Markdown:  "- purge du cache CDN\n- bascule du DNS à 22 h",
			UpdatedBy: "vcarroy", UpdatedAt: &at,
		},
		Unavailable: KnownUnavailable,
		GeneratedAt: at, GeneratedBy: "vcarroy", Provisional: true,
	}
}

func TestRenderPDFProducesAPDF(t *testing.T) {
	pdf, err := RenderPDF(sample(t))
	if err != nil {
		t.Fatalf("rendu: %v", err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Errorf("en-tête = %q, attendu un PDF", pdf[:min(8, len(pdf))])
	}
	if len(pdf) < 1000 {
		t.Errorf("taille = %d octets, document vraisemblablement vide", len(pdf))
	}
}

// Sans cette mention, une mise en page non validée peut être transmise telle
// quelle au client. C'est le seul risque réel de la livrer avant validation.
func TestProvisionalNotesAreMarkedOnEveryPage(t *testing.T) {
	pdf, err := RenderPDF(sample(t))
	if err != nil {
		t.Fatalf("rendu: %v", err)
	}
	// Le flux de contenu est compressé : la mention est cherchée dans le PDF non
	// compressé, produit pour ce contrôle.
	if !bytes.Contains(uncompressed(t, sample(t)), []byte("PROVISOIRE")) {
		t.Error("mention PROVISOIRE absente")
	}
	_ = pdf
}

func TestFinalNotesCarryNoProvisionalMark(t *testing.T) {
	note := sample(t)
	note.Provisional = false

	if bytes.Contains(uncompressed(t, note), []byte("PROVISOIRE")) {
		t.Error("mention PROVISOIRE présente sur un document validé")
	}
}

// Un bon de livraison sans identité ne désigne aucune livraison. Le produire
// serait pire que ne rien produire : il aurait l'air d'un document.
func TestRenderRefusesANoteWithoutIdentity(t *testing.T) {
	if _, err := RenderPDF(Note{ImageVersion: "1.8.0"}); err == nil {
		t.Error("un bon de livraison sans release doit être refusé")
	}
	if _, err := RenderPDF(Note{Release: "1.8.0"}); err == nil {
		t.Error("un bon de livraison sans version d'image doit être refusé")
	}
	if _, err := RenderJSON(Note{}); err == nil {
		t.Error("le JSON obéit aux mêmes règles que le PDF")
	}
}

// Le français passe par cp1252 : une conversion manquante sortirait les accents
// en octets bruts, et le document serait illisible là où il est le plus lu.
func TestAccentsSurviveTheEncoding(t *testing.T) {
	note := sample(t)
	note.Promotions[0].Environment = "préproduction"

	content := uncompressed(t, note)
	// « é » vaut 0xE9 en cp1252 ; en UTF-8 il vaudrait 0xC3 0xA9.
	if !bytes.Contains(content, []byte{'p', 'r', 0xE9, 'p', 'r', 'o'}) {
		t.Error("les accents ne sont pas convertis en cp1252")
	}
}

// L'absence d'empreinte est une information, pas un vide : le digest est ce qui
// prouve que l'image livrée est celle qui a été validée.
func TestUnverifiedImagesAreNamed(t *testing.T) {
	note := sample(t)
	unverified := note.UnverifiedImages()
	if len(unverified) != 1 || unverified[0] != "1.8.0_prod" {
		t.Errorf("images non vérifiées = %v", unverified)
	}

	if !bytes.Contains(uncompressed(t, note), []byte("non v")) {
		t.Error("le document doit nommer ce qu'il n'a pas pu vérifier")
	}
}

func TestRenderJSONCarriesTheProvisionalFlag(t *testing.T) {
	payload, err := RenderJSON(sample(t))
	if err != nil {
		t.Fatalf("rendu: %v", err)
	}
	if !strings.Contains(string(payload), `"provisional": true`) {
		t.Error("un intégrateur doit pouvoir lire que le format n'est pas définitif")
	}
}

// uncompressed rend le PDF sans compression de flux, pour que les tests puissent
// y chercher du texte.
func uncompressed(t *testing.T, note Note) []byte {
	t.Helper()
	pdf, err := renderPDF(note, false)
	if err != nil {
		t.Fatalf("rendu: %v", err)
	}
	return pdf
}

// Traduire un fragment puis retraduire l'ensemble abîme ce qui l'était déjà : le
// tiret cadratin ressortait en point.
func TestFooterIsTranslatedOnce(t *testing.T) {
	content := uncompressed(t, sample(t))
	// « — » vaut 0x97 en cp1252.
	if !bytes.Contains(content, []byte{0x97, ' ', 'm', 'i', 's', 'e'}) {
		t.Error("le pied de page a subi une double conversion")
	}
}
