package results

import (
	"encoding/json"
	"strings"
)

/*
Marqueurs de flux — `##META##` et `##SUMMARY##`, éventuellement préfixés.

Deux informations doivent remonter **avant la fin du run** : le descripteur
d'exécution et la ligne de résumé. Elles sont trop petites pour justifier un
artefact, et trop utiles pour attendre — un opérateur qui regarde une campagne de
quarante minutes doit voir « PHPStan : 12 violations » dès que PHPStan a fini,
sans attendre les E2E (P2, doc 10).

Le canal ne transporte que du kilo-octet. Les payloads complets passent par les
URLs pré-signées : les logs conteneur subissent la rotation du kubelet et ne sont
pas un transport fiable pour plusieurs mégaoctets.

**Le descripteur est produit par le pipeline, jamais déduit ici.** Lui seul peut
distinguer « l'outil a échoué » de « l'outil n'a rien trouvé » — les deux
produisent zéro violation, et les confondre ferait passer un scanner planté pour
un code sain. C'est la meilleure propriété du `security-audit` existant, et elle
est reprise telle quelle.
*/

// MarkerSet nomme les deux marqueurs qu'un flux emploie.
//
// Un jeton et non deux chaînes libres : les deux marqueurs d'un même protocole
// doivent porter la même marque, et laisser l'appelant les composer permettrait
// un `##ACME-META##` avec un `##SUMMARY##`, dont rien ne dirait qu'ils vont
// ensemble.
type MarkerSet struct {
	Meta    string
	Summary string
}

// DefaultMarkers est le jeu sans marque de projet.
//
// Les marqueurs valaient `##MSE-META##` et `##MSE-SUMMARY##`, en constantes. Le
// nom d'un projet dans un **protocole** est un couplage d'une autre nature que
// dans une interface : un pipeline générique qui émet `##MSE-META##` reste marqué
// MSE dans son code source, et c'est précisément ce qu'on cherche à retirer
// (doc 12 §12.9, chantier 5).
var DefaultMarkers = NewMarkerSet("")

// NewMarkerSet compose les marqueurs d'un jeton de projet.
//
// Jeton vide pour la forme neutre — `##META##`. Le jeton `MSE` rend la forme
// historique, `##MSE-META##`, ce qui laisse un déploiement qui l'émet déjà
// continuer sans rien changer à ses pipelines.
func NewMarkerSet(token string) MarkerSet {
	token = strings.TrimSpace(strings.Trim(strings.TrimSpace(token), "-"))
	if token != "" {
		token += "-"
	}
	return MarkerSet{Meta: "##" + token + "META##", Summary: "##" + token + "SUMMARY##"}
}

// Meta est le descripteur d'exécution d'un outil.
type Meta struct {
	Tool    string `json:"tool"`
	Version string `json:"version,omitempty"`
	// ExitCode est celui de l'outil. Distinct de `Status` : un `exit_code` non nul
	// peut être normal — PHPStan sort en 1 dès qu'il trouve une violation, ce qui
	// est un succès de l'outil et un échec du code.
	ExitCode int `json:"exit_code"`
	// Status vaut `ok` quand l'outil a fait son travail, quel qu'en soit le
	// résultat. Toute autre valeur signale que l'outil n'a pas pu conclure, et le
	// signal correspondant devient « non mesuré » plutôt que « conforme ».
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// Ran dit si l'outil a conclu.
//
// Un descripteur absent vaut « n'a pas conclu » : le pipeline est censé en
// émettre un, et son absence est elle-même une information. Se tromper dans ce
// sens affiche « non mesuré » sur un outil qui a tourné ; dans l'autre, il
// afficherait « conforme » sur un scanner qui a planté.
func (m Meta) Ran() bool { return strings.EqualFold(m.Status, "ok") }

// Markers rassemble ce qu'un flux a livré.
type Markers struct {
	// Metas est indexé par outil. Un outil qui en émet plusieurs — reprise après
	// échec — voit le dernier faire foi : c'est celui qui décrit l'exécution
	// retenue.
	Metas map[string]Meta
	// Summaries conserve l'ordre d'apparition, qui est l'ordre d'exécution.
	Summaries []string
}

// ScanMarkers extrait les marqueurs d'un flux de journal.
//
// Tolérant sur la forme : un marqueur mal formé est ignoré plutôt que de faire
// échouer la lecture du journal. Le journal a d'autres usages que celui-ci, et le
// perdre en entier pour une accolade manquante serait disproportionné.
func (m MarkerSet) ScanMarkers(text string) Markers {
	markers := Markers{Metas: map[string]Meta{}}

	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, m.Meta):
			payload := strings.TrimSpace(strings.TrimPrefix(trimmed, m.Meta))
			var meta Meta
			if err := json.Unmarshal([]byte(payload), &meta); err != nil || meta.Tool == "" {
				continue
			}
			markers.Metas[meta.Tool] = meta

		case strings.HasPrefix(trimmed, m.Summary):
			summary := strings.TrimSpace(strings.TrimPrefix(trimmed, m.Summary))
			if summary != "" {
				markers.Summaries = append(markers.Summaries, summary)
			}
		}
	}

	return markers
}

// StripMarkers retire les lignes de marqueur d'un journal.
//
// Le journal affiché est celui que l'utilisateur lit : y laisser des lignes de
// protocole le charge sans rien lui apprendre, puisque l'information qu'elles
// portent est affichée à côté, mise en forme.
func (m MarkerSet) StripMarkers(text string) string {
	lines := strings.Split(text, "\n")
	kept := lines[:0]

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, m.Meta) || strings.HasPrefix(trimmed, m.Summary) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
