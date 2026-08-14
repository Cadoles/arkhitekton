package report

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/go-pdf/fpdf"
)

/*
Rendu PDF du bon de livraison.

**La mise en page ci-dessous est provisoire.** La doc 09 l'établit : le format
exact du bon de livraison doit être validé avec le client avant développement,
parce que c'est un document contractuel et que sa présentation n'est pas une
décision technique. Ce fichier existe donc pour que la chaîne complète — demande,
génération, stockage, téléchargement — soit éprouvée de bout en bout, et pour
donner au client quelque chose à corriger plutôt qu'une page blanche à imaginer.

Deux conséquences assumées :

  - chaque page porte la mention « PROVISOIRE ». Un document sans elle pourrait
    être transmis tel quel, et c'est le seul risque réel de livrer une mise en
    page non validée ;
  - aucune donnée ne vit ici. Tout vient de [Note], et remplacer ce rendu ne
    demandera pas de retoucher ce qui le nourrit.

Police de base plutôt qu'une police embarquée : Helvetica est dans tout lecteur,
son rendu ne dépend de rien, et le poids du document reste de quelques kilo-octets.
Le prix est un jeu de caractères limité à cp1252 — suffisant pour le français, et
la translittération est explicite plutôt que silencieuse.
*/

const (
	marginX    = 15.0
	lineHeight = 5.0
)

// RenderPDF rend le bon de livraison en PDF.
func RenderPDF(note Note) ([]byte, error) {
	return renderPDF(note, true)
}

// renderPDF porte le rendu. `compress` n'existe que pour les tests, qui doivent
// pouvoir chercher du texte dans le document produit.
func renderPDF(note Note, compress bool) ([]byte, error) {
	if err := note.Validate(); err != nil {
		return nil, err
	}

	doc := fpdf.New("P", "mm", "A4", "")
	doc.SetCompression(compress)
	doc.SetMargins(marginX, 15, marginX)
	doc.SetAutoPageBreak(true, 20)

	// Les accents français sortiraient en octets bruts sans cette conversion :
	// les polices de base sont en cp1252, pas en UTF-8.
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
		// Le point le plus contre-intuitif du dispositif, et celui qu'un lecteur
		// du document doit comprendre sans avoir à le demander.
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
				// Nommé et non omis : le digest est la preuve que l'image livrée
				// est celle qui a été validée. Son absence est une information.
				digest = "empreinte non vérifiée"
			}
			bullet(doc, write, fmt.Sprintf("%s:%s", image.Repository, image.Tag), digest)
		}
	}

	section(doc, write, "Déploiements demandés")
	if len(note.Promotions) == 0 {
		paragraph(doc, write, "Aucun déploiement n'a été demandé pour cette release.")
	} else {
		for _, promotion := range note.Promotions {
			bullet(doc, write, promotion.Environment, fmt.Sprintf("%s, demandé le %s",
				promotion.Tag, promotion.RequestedAt.Format("02/01/2006 à 15:04")))
		}
		paragraph(doc, write,
			"Une demande de déploiement pousse un tag dans le registre ; la mise en service est ensuite "+
				"réalisée par l'outil de déploiement continu. Ce document atteste des demandes, non de leur "+
				"aboutissement.")
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
		return nil, fmt.Errorf("rendu du bon de livraison %q: %w", note.Release, err)
	}
	return buffer.Bytes(), nil
}

func header(doc *fpdf.Fpdf, write func(string) string, note Note) {
	doc.SetFont("Helvetica", "B", 14)
	doc.SetY(12)
	doc.Cell(0, 7, write("Bon de livraison — "+note.Release))

	if note.Provisional {
		// À droite du titre, sur chaque page : c'est la seule protection contre
		// une transmission au client d'une mise en page non validée.
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

	// Une seule conversion : traduire un fragment puis retraduire l'ensemble
	// abîme les accents et les tirets cadratins déjà convertis.
	left := note.Release
	if note.Provisional {
		left += " — mise en page provisoire, en attente de validation"
	}
	doc.CellFormat(0, 5, write(left), "", 0, "L", false, 0, "")
	// La pagination compte dans un document contractuel : elle prouve qu'aucune
	// page n'a été retirée.
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
	// MultiCell coupe aux limites de la largeur : un digest ou une URL longue
	// déborderait sinon hors de la page, silencieusement.
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

	// Deux traits, pas un formulaire : la forme exacte du bloc de signature fait
	// partie de ce qui doit être validé.
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
