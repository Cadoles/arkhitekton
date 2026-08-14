package results

import "strings"

import "testing"

const stream = `Running PHPStan...
##MSE-META## {"tool":"phpstan","version":"1.11.2","exit_code":1,"status":"ok"}
##MSE-SUMMARY## 12 violations dans 4 fichiers
Note: 12 errors found
##MSE-META## {"tool":"trivy","exit_code":0,"status":"error","error":"db download failed"}
##MSE-SUMMARY## audit interrompu
`

func TestScanMarkers(t *testing.T) {
	markers := ScanMarkers(stream)

	if len(markers.Metas) != 2 {
		t.Fatalf("%d descripteur(s)", len(markers.Metas))
	}
	if len(markers.Summaries) != 2 || markers.Summaries[0] != "12 violations dans 4 fichiers" {
		t.Errorf("résumés = %v", markers.Summaries)
	}
}

// PHPStan sort en 1 dès qu'il trouve une violation : c'est un succès de l'outil
// et un échec du code. Confondre les deux rendrait toute analyse non conforme
// « non mesurée ».
func TestANonZeroExitCodeIsNotAToolFailure(t *testing.T) {
	markers := ScanMarkers(stream)

	phpstan := markers.Metas["phpstan"]
	if phpstan.ExitCode != 1 {
		t.Errorf("code de sortie = %d", phpstan.ExitCode)
	}
	if !phpstan.Ran() {
		t.Error("phpstan a conclu : son signal doit être mesuré")
	}
}

// Le scanner planté et le scanner qui ne trouve rien produisent tous deux zéro
// finding. Le descripteur est ce qui les distingue (P2).
func TestAFailedToolIsNotMeasured(t *testing.T) {
	markers := ScanMarkers(stream)

	if markers.Metas["trivy"].Ran() {
		t.Error("un outil en erreur ne doit pas compter comme mesuré")
	}
}

// Un descripteur absent vaut « n'a pas conclu ». Se tromper dans ce sens affiche
// « non mesuré » sur un outil qui a tourné ; dans l'autre, « conforme » sur un
// scanner qui a planté.
func TestAnAbsentMetaIsNotARun(t *testing.T) {
	if (Meta{}).Ran() {
		t.Error("un descripteur vide ne doit pas valoir exécution")
	}
}

// Le journal a d'autres usages : le perdre en entier pour une accolade manquante
// serait disproportionné.
func TestMalformedMarkersAreIgnoredNotFatal(t *testing.T) {
	markers := ScanMarkers("##MSE-META## {pas du json\n##MSE-SUMMARY##\nligne normale\n")

	if len(markers.Metas) != 0 {
		t.Errorf("descripteurs = %v", markers.Metas)
	}
	// Un résumé vide n'est pas un résumé.
	if len(markers.Summaries) != 0 {
		t.Errorf("résumés = %v", markers.Summaries)
	}
}

// Une reprise après échec émet deux descripteurs : le dernier décrit
// l'exécution retenue.
func TestTheLastMetaWins(t *testing.T) {
	markers := ScanMarkers(`##MSE-META## {"tool":"phpunit","status":"error"}
##MSE-META## {"tool":"phpunit","status":"ok"}`)

	if !markers.Metas["phpunit"].Ran() {
		t.Error("le dernier descripteur doit faire foi")
	}
}

// Le journal affiché est celui que l'utilisateur lit : y laisser des lignes de
// protocole le charge sans rien lui apprendre.
func TestStripMarkersKeepsTheRest(t *testing.T) {
	cleaned := StripMarkers(stream)

	if strings.Contains(cleaned, MarkerMeta) || strings.Contains(cleaned, MarkerSummary) {
		t.Error("les marqueurs doivent disparaître du journal affiché")
	}
	if !strings.Contains(cleaned, "Running PHPStan...") {
		t.Error("le reste du journal doit être conservé")
	}
}
