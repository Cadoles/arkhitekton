package results

import "strings"

import "testing"

const stream = `Running PHPStan...
##META## {"tool":"phpstan","version":"1.11.2","exit_code":1,"status":"ok"}
##SUMMARY## 12 violations dans 4 fichiers
Note: 12 errors found
##META## {"tool":"trivy","exit_code":0,"status":"error","error":"db download failed"}
##SUMMARY## audit interrompu
`

func TestScanMarkers(t *testing.T) {
	markers := DefaultMarkers.ScanMarkers(stream)

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
	markers := DefaultMarkers.ScanMarkers(stream)

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
	markers := DefaultMarkers.ScanMarkers(stream)

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
	markers := DefaultMarkers.ScanMarkers("##META## {pas du json\n##SUMMARY##\nligne normale\n")

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
	markers := DefaultMarkers.ScanMarkers(`##META## {"tool":"phpunit","status":"error"}
##META## {"tool":"phpunit","status":"ok"}`)

	if !markers.Metas["phpunit"].Ran() {
		t.Error("le dernier descripteur doit faire foi")
	}
}

// Le journal affiché est celui que l'utilisateur lit : y laisser des lignes de
// protocole le charge sans rien lui apprendre.
func TestStripMarkersKeepsTheRest(t *testing.T) {
	cleaned := DefaultMarkers.StripMarkers(stream)

	if strings.Contains(cleaned, DefaultMarkers.Meta) || strings.Contains(cleaned, DefaultMarkers.Summary) {
		t.Error("les marqueurs doivent disparaître du journal affiché")
	}
	if !strings.Contains(cleaned, "Running PHPStan...") {
		t.Error("le reste du journal doit être conservé")
	}
}

// Un jeton de projet rend la forme historique, ce qui laisse un déploiement qui
// émet déjà `##MSE-META##` continuer sans toucher à ses pipelines.
func TestUnJetonDeProjetRendLaFormeHistorique(t *testing.T) {
	mse := NewMarkerSet("MSE")

	if mse.Meta != "##MSE-META##" || mse.Summary != "##MSE-SUMMARY##" {
		t.Fatalf("marqueurs = %q / %q", mse.Meta, mse.Summary)
	}

	markers := mse.ScanMarkers(`##MSE-META## {"tool":"phpstan","status":"ok"}
##MSE-SUMMARY## 12 violations`)
	if !markers.Metas["phpstan"].Ran() || len(markers.Summaries) != 1 {
		t.Errorf("marqueurs lus = %+v", markers)
	}

	// Et le jeu neutre ne les lit pas : deux protocoles distincts ne doivent pas
	// se confondre, sans quoi le jeton ne servirait à rien.
	if len(DefaultMarkers.ScanMarkers("##MSE-META## {\"tool\":\"phpstan\"}").Metas) != 0 {
		t.Error("le jeu neutre ne doit pas lire les marqueurs d'un autre jeton")
	}
}
