package report

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/go-pdf/fpdf"
)

/*
 * PDF rendering of the delivery note.
 *
 * **The layout below is provisional.** The exact format of the delivery note must be validated
 * with the client before development, because it is a contractual document and its presentation
 * is not a technical decision. This file therefore exists so that the whole chain — request,
 * generation, storage, download — is exercised end to end, and to give the client something to
 * correct rather than a blank page to imagine.
 *
 * Two accepted consequences:
 *
 * - every page carries the mention "PROVISOIRE". A document without it could be passed on as it
 * is, and that is the only real risk of delivering a layout that is not validated;
 * - no data lives here. Everything comes from [Note], and replacing this rendering will not
 * require touching what feeds it.
 *
 * A base font rather than an embedded one: Helvetica is in every reader, its rendering depends on
 * nothing, and the document's weight stays a few kilobytes. The price is a character set limited
 * to cp1252 — enough for French, and the transliteration is explicit rather than silent.
 */

const (
	marginX    = 15.0
	lineHeight = 5.0
)

// RenderPDF renders the delivery note as a PDF.
func RenderPDF(note Note) ([]byte, error) {
	return renderPDF(note, true)
}

// renderPDF carries the rendering. `compress` exists only for the tests, which must be able to
// look for text in the document produced.
func renderPDF(note Note, compress bool) ([]byte, error) {
	if err := note.Validate(); err != nil {
		return nil, err
	}

	doc := fpdf.New("P", "mm", "A4", "")
	doc.SetCompression(compress)
	doc.SetMargins(marginX, 15, marginX)
	doc.SetAutoPageBreak(true, 20)

	// French accents would come out as raw bytes without this conversion: the base fonts are cp1252,
	// not UTF-8.
	tr := doc.UnicodeTranslatorFromDescriptor("")
	write := func(text string) string { return tr(text) }

	doc.SetHeaderFunc(func() { header(doc, write, note) })
	doc.SetFooterFunc(func() { footer(doc, write, note) })
	doc.AddPage()

	section(doc, write, "Livraison")
	fields(doc, write, [][2]string{
		{"Release", note.Release},
		{"Version", note.Version},
		{"Nature", kindLabel(note.Kind)},
		{"Version de l'image", note.ImageVersion},
	})

	if note.SourceTag != "" {
		// The most counter-intuitive point of the arrangement, and the one a reader of the document must
		// understand without having to ask.
		paragraph(doc, write, fmt.Sprintf(
			"Cette release est un re-tag de %s : l'image livrée est, octet pour octet, celle qui a été "+
				"validée. Elle porte donc encore la version %s, qui est ce que rapportera l'application "+
				"une fois déployée.", note.SourceTag, note.ImageVersion))
	}
	if note.GitRef != "" {
		fields(doc, write, [][2]string{{"Référence Git", note.GitRef}})
	}

	section(doc, write, "Images livrées")
	if len(note.Images) == 0 {
		paragraph(doc, write, "Aucune image n'est rattachée à cette release.")
	} else {
		for _, image := range note.Images {
			digest := image.Digest
			if digest == "" {
				// Named and not omitted: the digest is the proof that the image delivered is the one that was
				// validated. Its absence is information.
				digest = "empreinte non vérifiée"
			}
			bullet(doc, write, fmt.Sprintf("%s:%s", image.Repository, image.Tag), digest)
		}
	}

	section(doc, write, "Actions manuelles")
	if note.ManualActions == nil || strings.TrimSpace(note.ManualActions.Markdown) == "" {
		paragraph(doc, write, "Aucune action manuelle n'a été consignée.")
	} else {
		for line := range strings.SplitSeq(note.ManualActions.Markdown, "\n") {
			paragraph(doc, write, line)
		}
		if note.ManualActions.UpdatedBy != "" {
			attribution := note.ManualActions.UpdatedBy
			if note.ManualActions.UpdatedAt != nil {
				attribution += ", " + note.ManualActions.UpdatedAt.Format("02/01/2006 à 15:04")
			}
			paragraph(doc, write, "Consigné par "+attribution+".")
		}
	}

	if len(note.Unavailable) > 0 {
		section(doc, write, "Éléments non attestés")
		paragraph(doc, write,
			"Les éléments suivants ne figurent pas dans ce document parce que le système ne les connaît "+
				"pas encore. Leur absence n'est pas un constat de conformité.")
		for _, entry := range note.Unavailable {
			bullet(doc, write, entry, "")
		}
	}

	section(doc, write, "Signature")
	paragraph(doc, write, fmt.Sprintf("Document établi le %s par %s.",
		note.GeneratedAt.Format("02/01/2006 à 15:04"), note.GeneratedBy))
	signatureBlock(doc, write)

	var buffer bytes.Buffer
	if err := doc.Output(&buffer); err != nil {
		return nil, fmt.Errorf("rendering the delivery note %q: %w", note.Release, err)
	}
	return buffer.Bytes(), nil
}

func header(doc *fpdf.Fpdf, write func(string) string, note Note) {
	doc.SetFont("Helvetica", "B", 14)
	doc.SetY(12)
	doc.Cell(0, 7, write("Bon de livraison — "+note.Release))

	if note.Provisional {
		// To the right of the title, on every page: it is the only protection against a layout that is not
		// validated being sent to the client.
		doc.SetFont("Helvetica", "B", 9)
		doc.SetTextColor(180, 30, 30)
		doc.SetXY(-marginX-40, 13)
		doc.CellFormat(40, 5, write("PROVISOIRE"), "", 0, "R", false, 0, "")
		doc.SetTextColor(0, 0, 0)
	}

	doc.Ln(12)
}

func footer(doc *fpdf.Fpdf, write func(string) string, note Note) {
	doc.SetY(-15)
	doc.SetFont("Helvetica", "", 8)
	doc.SetTextColor(110, 110, 110)

	// A single conversion: translating a fragment then translating the whole again damages the accents
	// and the em dashes already converted.
	left := note.Release
	if note.Provisional {
		left += " — mise en page provisoire, en attente de validation"
	}
	doc.CellFormat(0, 5, write(left), "", 0, "L", false, 0, "")
	// Pagination counts in a contractual document: it proves that no page was removed.
	doc.CellFormat(0, 5, write(fmt.Sprintf("Page %d / {nb}", doc.PageNo())), "", 0, "R", false, 0, "")
	doc.AliasNbPages("{nb}")
	doc.SetTextColor(0, 0, 0)
}

func section(doc *fpdf.Fpdf, write func(string) string, title string) {
	doc.Ln(3)
	doc.SetFont("Helvetica", "B", 11)
	doc.Cell(0, 6, write(title))
	doc.Ln(7)
	doc.SetFont("Helvetica", "", 10)
}

func fields(doc *fpdf.Fpdf, write func(string) string, rows [][2]string) {
	for _, row := range rows {
		if row[1] == "" {
			continue
		}
		doc.SetFont("Helvetica", "", 10)
		doc.SetTextColor(110, 110, 110)
		doc.CellFormat(45, lineHeight, write(row[0]), "", 0, "L", false, 0, "")
		doc.SetTextColor(0, 0, 0)
		doc.CellFormat(0, lineHeight, write(row[1]), "", 0, "L", false, 0, "")
		doc.Ln(lineHeight)
	}
}

func paragraph(doc *fpdf.Fpdf, write func(string) string, text string) {
	doc.SetFont("Helvetica", "", 10)
	// MultiCell cuts at the width's bounds: a digest or a long URL would otherwise overflow off the
	// page, silently.
	doc.MultiCell(0, lineHeight, write(text), "", "L", false)
	doc.Ln(1)
}

func bullet(doc *fpdf.Fpdf, write func(string) string, label, detail string) {
	doc.SetFont("Helvetica", "", 10)
	doc.CellFormat(5, lineHeight, write("•"), "", 0, "L", false, 0, "")
	if detail == "" {
		doc.MultiCell(0, lineHeight, write(label), "", "L", false)
		return
	}
	doc.MultiCell(0, lineHeight, write(label+"  —  "+detail), "", "L", false)
}

func signatureBlock(doc *fpdf.Fpdf, write func(string) string) {
	doc.Ln(6)
	doc.SetFont("Helvetica", "", 10)
	doc.SetTextColor(110, 110, 110)
	doc.CellFormat(85, lineHeight, write("Pour le prestataire"), "", 0, "L", false, 0, "")
	doc.CellFormat(0, lineHeight, write("Pour le client"), "", 0, "L", false, 0, "")
	doc.Ln(lineHeight + 12)
	doc.SetTextColor(0, 0, 0)

	// Two lines, not a form: the exact shape of the signature block is part of what must be validated.
	y := doc.GetY()
	doc.Line(marginX, y, marginX+70, y)
	doc.Line(marginX+100, y, marginX+170, y)
}

func kindLabel(kind string) string {
	switch kind {
	case "pre":
		return "Pré-release"
	case "final":
		return "Release"
	case "patch":
		return "Correctif"
	default:
		return kind
	}
}
