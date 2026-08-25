package worker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/logs"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/results"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

// fakeLogSource sert un journal par task, en une seule tranche.
type fakeLogSource struct {
	byTask map[string]string
	// slice force la lecture par tranches, pour éprouver la boucle de
	// pagination : le lecteur réel ne rend pas tout d'un coup.
	slice int
}

func (f *fakeLogSource) From(
	_ context.Context, _ string, taskName string, offset, _ int64,
) (logs.Segment, error) {
	text, found := f.byTask[taskName]
	if !found {
		return logs.Segment{}, errors.New("journal absent")
	}

	size := int64(len(text))
	if offset >= size {
		return logs.Segment{Offset: size, NextOffset: size, Size: size, Sealed: true}, nil
	}

	end := size
	if f.slice > 0 && offset+int64(f.slice) < size {
		end = offset + int64(f.slice)
	}
	return logs.Segment{
		Content: []byte(text[offset:end]), Offset: offset,
		NextOffset: end, Size: size, Sealed: true,
	}, nil
}

type fakeMarkerStore struct {
	saved    []store.TestResult
	existing map[string]bool
}

func (f *fakeMarkerStore) SaveTestResult(
	_ context.Context, result store.TestResult, _ []store.TestCase,
) (int64, error) {
	f.saved = append(f.saved, result)
	return int64(len(f.saved)), nil
}

func (f *fakeMarkerStore) TestResultExists(_ context.Context, _ int64, tool string) (bool, error) {
	return f.existing[tool], nil
}

func markerIngester(t *testing.T, source *fakeLogSource) (*MarkerIngester, *fakeMarkerStore) {
	t.Helper()
	db := &fakeMarkerStore{existing: map[string]bool{}}
	return NewMarkerIngester(db, source, results.NewMarkerSet("MSE"), quietLogger()), db
}

// Le cas que le canal existait pour préserver : un scanner planté publie zéro
// violation, exactement comme un code sain. Sans le descripteur, il s'affichait
// « conforme ».
func TestUnOutilQuiNaPasConcluDevientNonMesure(t *testing.T) {
	ingester, db := markerIngester(t, &fakeLogSource{byTask: map[string]string{
		"audit": `Running trivy...
##MSE-META## {"tool":"trivy","exit_code":0,"status":"error","error":"db download failed"}
##MSE-SUMMARY## audit interrompu
`,
	}})

	err := ingester.Ingest(context.Background(), MarkerRun{
		ID: 7, Name: "security-audit-run-abc", Tasks: []string{"audit"}, Family: "security",
	})
	if err != nil {
		t.Fatalf("ingestion: %v", err)
	}

	if len(db.saved) != 1 {
		t.Fatalf("%d résultat(s) enregistré(s), attendu 1: %+v", len(db.saved), db.saved)
	}
	saved := db.saved[0]
	// `na` et non `fail` : ce n'est pas un verdict sur le code, c'est l'absence de
	// verdict. Le score sort ce signal du calcul au lieu de compter un zéro faux.
	if saved.Status != results.ReportNA {
		t.Errorf("statut = %q, attendu %q", saved.Status, results.ReportNA)
	}
	if saved.Tool != "trivy" || saved.Family != "security" || saved.RunID != 7 {
		t.Errorf("résultat = %+v", saved)
	}
	// La cause vient du pipeline : une cellule vide se lirait comme une absence de
	// mesure sans raison, ce qui est la moitié de l'information.
	if saved.Summary != "db download failed" {
		t.Errorf("résumé = %q", saved.Summary)
	}
}

// Un outil qui a conclu n'est pas enregistré ici : son artefact porte le
// décompte, et en tirer un second résultat donnerait deux lignes pour une
// exécution sans que rien ne dise laquelle fait foi.
func TestUnOutilQuiAConcluNestPasEnregistre(t *testing.T) {
	ingester, db := markerIngester(t, &fakeLogSource{byTask: map[string]string{
		"tests": `##MSE-META## {"tool":"phpstan","exit_code":1,"status":"ok"}` + "\n",
	}})

	if err := ingester.Ingest(context.Background(), MarkerRun{
		ID: 8, Name: "static-run", Tasks: []string{"tests"}, Family: "static",
	}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}

	// `exit_code: 1` est un **succès** de PHPStan : il sort en 1 dès qu'il trouve
	// une violation. Confondre les deux ferait passer chaque analyse fructueuse
	// pour un outil en panne.
	if len(db.saved) != 0 {
		t.Errorf("un outil qui a conclu ne doit rien enregistrer ici: %+v", db.saved)
	}
}

// L'artefact l'emporte quand il existe : l'ingestion est idempotente par outil,
// et réenregistrer doublerait la ligne.
func TestUnResultatDejaPresentNestPasDouble(t *testing.T) {
	ingester, db := markerIngester(t, &fakeLogSource{byTask: map[string]string{
		"tests": `##MSE-META## {"tool":"phpunit","exit_code":255,"status":"error"}` + "\n",
	}})
	db.existing["phpunit"] = true

	if err := ingester.Ingest(context.Background(), MarkerRun{
		ID: 9, Name: "unit-run", Tasks: []string{"tests"}, Family: "unit",
	}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	if len(db.saved) != 0 {
		t.Errorf("aucun doublon attendu: %+v", db.saved)
	}
}

// Une task sans journal ne doit pas emporter les marqueurs des autres : elle a pu
// être sautée, ou son collecteur n'a rien scellé.
func TestUneTaskSansJournalNemporteFasLesAutres(t *testing.T) {
	ingester, db := markerIngester(t, &fakeLogSource{byTask: map[string]string{
		"seconde": `##MSE-META## {"tool":"gitleaks","status":"error","error":"timeout"}` + "\n",
	}})

	if err := ingester.Ingest(context.Background(), MarkerRun{
		ID: 10, Name: "audit-run", Tasks: []string{"absente", "seconde"}, Family: "security",
	}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	if len(db.saved) != 1 || db.saved[0].Tool != "gitleaks" {
		t.Errorf("résultats = %+v", db.saved)
	}
}

// Le journal est lu par tranches, le lecteur réel ne rendant pas tout d'un coup.
// Un marqueur à cheval sur deux tranches doit rester lisible.
func TestLeJournalEstLuJusquAuBout(t *testing.T) {
	descriptor := `##MSE-META## {"tool":"cypress","status":"error","error":"navigateur absent"}`
	ingester, db := markerIngester(t, &fakeLogSource{
		byTask: map[string]string{"e2e": strings.Repeat("bruit\n", 200) + descriptor + "\n"},
		slice:  17,
	})

	if err := ingester.Ingest(context.Background(), MarkerRun{
		ID: 11, Name: "e2e-run", Tasks: []string{"e2e"}, Family: "e2e",
	}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	if len(db.saved) != 1 || db.saved[0].Summary != "navigateur absent" {
		t.Errorf("résultats = %+v", db.saved)
	}
}

// Sans famille, rien n'est enregistré : un résultat qui ne se rattache à aucun
// signal n'apparaîtrait nulle part, et occuperait une ligne pour rien.
func TestSansFamilleRienNestEnregistre(t *testing.T) {
	ingester, db := markerIngester(t, &fakeLogSource{byTask: map[string]string{
		"build": `##MSE-META## {"tool":"buildx","status":"error"}` + "\n",
	}})

	if err := ingester.Ingest(context.Background(), MarkerRun{
		ID: 12, Name: "build-run", Tasks: []string{"build"},
	}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	if len(db.saved) != 0 {
		t.Errorf("aucun enregistrement attendu sans famille: %+v", db.saved)
	}
}

// La famille vient du catalogue, qui la déclare déjà pour ranger les artefacts.
// Un pipeline qui n'en déclare aucune, ou plusieurs, ne permet pas de classer.
func TestLaFamilleVientDuCatalogue(t *testing.T) {
	pipelines := &catalog.Catalog{Pipelines: []catalog.Pipeline{
		{Kind: "unit-tests", Uploads: map[string]catalog.Upload{"rapport": {Family: "unit"}}},
		{Kind: "build"},
		{Kind: "mixte", Uploads: map[string]catalog.Upload{"a": {Family: "unit"}, "b": {Family: "e2e"}}},
	}}

	if run, ok := markerRunFor(pipelines, "unit-tests", MarkerRun{ID: 1}); !ok || run.Family != "unit" {
		t.Errorf("unit-tests: ok=%v famille=%q", ok, run.Family)
	}
	if _, ok := markerRunFor(pipelines, "build", MarkerRun{ID: 1}); ok {
		t.Error("un pipeline sans artefact ne mesure aucune famille")
	}
	// Deux familles : rien ne dit à laquelle rattacher un outil, et classer au
	// hasard déplacerait un verdict sur un signal qui n'a pas été mesuré.
	if _, ok := markerRunFor(pipelines, "mixte", MarkerRun{ID: 1}); ok {
		t.Error("un pipeline à deux familles ne permet pas de classer")
	}
	if _, ok := markerRunFor(nil, "unit-tests", MarkerRun{ID: 1}); ok {
		t.Error("sans catalogue, rien n'est classable")
	}
}
