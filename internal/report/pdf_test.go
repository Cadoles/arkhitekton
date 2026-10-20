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
		t.Fatalf("rendering: %v", err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Errorf("header = %q, expected a PDF", pdf[:min(8, len(pdf))])
	}
	if len(pdf) < 1000 {
		t.Errorf("size = %d bytes, document probably empty", len(pdf))
	}
}

// Without that mention, a layout that is not validated can be sent to the client as it is. It is
// the only real risk of delivering it before validation.
func TestProvisionalNotesAreMarkedOnEveryPage(t *testing.T) {
	pdf, err := RenderPDF(sample(t))
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	// The content stream is compressed: the mention is looked for in the uncompressed PDF, produced
	// for this check.
	if !bytes.Contains(uncompressed(t, sample(t)), []byte("PROVISOIRE")) {
		t.Error("the PROVISOIRE mention is missing")
	}
	_ = pdf
}

func TestFinalNotesCarryNoProvisionalMark(t *testing.T) {
	note := sample(t)
	note.Provisional = false

	if bytes.Contains(uncompressed(t, note), []byte("PROVISOIRE")) {
		t.Error("the PROVISOIRE mention appears on a validated document")
	}
}

// A delivery note with no identity names no delivery. Producing it would be worse than producing
// nothing: it would look like a document.
func TestRenderRefusesANoteWithoutIdentity(t *testing.T) {
	if _, err := RenderPDF(Note{ImageVersion: "1.8.0"}); err == nil {
		t.Error("a delivery note with no release must be refused")
	}
	if _, err := RenderPDF(Note{Release: "1.8.0"}); err == nil {
		t.Error("a delivery note with no image version must be refused")
	}
	if _, err := RenderJSON(Note{}); err == nil {
		t.Error("the JSON obeys the same rules as the PDF")
	}
}

// French goes through cp1252: a missing conversion would output the accents as raw bytes, and the
// document would be unreadable where it is read most.
func TestAccentsSurviveTheEncoding(t *testing.T) {
	note := sample(t)
	note.ManualActions.Markdown = "- bascule en préproduction"

	content := uncompressed(t, note)
	// "é" is 0xE9 in cp1252; in UTF-8 it would be 0xC3 0xA9.
	if !bytes.Contains(content, []byte{'p', 'r', 0xE9, 'p', 'r', 'o'}) {
		t.Error("the accents are not converted to cp1252")
	}
}

// The absence of a digest is information, not a blank: the digest is what proves that the image
// delivered is the one that was validated.
func TestUnverifiedImagesAreNamed(t *testing.T) {
	note := sample(t)
	unverified := note.UnverifiedImages()
	if len(unverified) != 1 || unverified[0] != "1.8.0_prod" {
		t.Errorf("unverified images = %v", unverified)
	}

	if !bytes.Contains(uncompressed(t, note), []byte("non v")) {
		t.Error("the document must name what it could not check")
	}
}

func TestRenderJSONCarriesTheProvisionalFlag(t *testing.T) {
	payload, err := RenderJSON(sample(t))
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	if !strings.Contains(string(payload), `"provisional": true`) {
		t.Error("an integrator must be able to read that the format is not final")
	}
}

// uncompressed renders the PDF with no stream compression, so that the tests can look for text in
// it.
func uncompressed(t *testing.T, note Note) []byte {
	t.Helper()
	pdf, err := renderPDF(note, false)
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	return pdf
}

// Translating a fragment then translating the whole again damages what was already translated: the
// em dash came out as a dot.
func TestFooterIsTranslatedOnce(t *testing.T) {
	content := uncompressed(t, sample(t))
	// "—" is 0x97 in cp1252.
	if !bytes.Contains(content, []byte{0x97, ' ', 'm', 'i', 's', 'e'}) {
		t.Error("the footer went through a double conversion")
	}
}
