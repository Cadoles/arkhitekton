package worker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"log/slog"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/results"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

type fakeTestStore struct {
	saved []store.TestResult
	cases [][]store.TestCase
	known map[string]bool
}

func (f *fakeTestStore) SaveTestResult(
	_ context.Context, result store.TestResult, cases []store.TestCase,
) (int64, error) {
	f.saved = append(f.saved, result)
	f.cases = append(f.cases, cases)
	return int64(len(f.saved)), nil
}

func (f *fakeTestStore) TestResultExists(_ context.Context, _ int64, tool string) (bool, error) {
	return f.known[tool], nil
}

const junitXML = `<testsuites><testsuite name="Unit" time="1.0">
  <testcase name="ok" classname="A" time="0.5"/>
  <testcase name="ko" classname="A" time="0.5"><failure message="boom">trace</failure></testcase>
</testsuite></testsuites>`

const phpstanJSON = `{"totals":{"errors":0,"file_errors":1},"files":{
  "src/A.php":{"errors":1,"messages":[{"message":"m","line":3,"identifier":"r"}]}},"errors":[]}`

// archive fabrique un tar.gz, forme sous laquelle un répertoire est publié.
func archive(t *testing.T, files map[string]string) string {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gz)

	for name, content := range files {
		if err := writer.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("archive: %v", err)
		}
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatalf("archive: %v", err)
		}
	}
	writer.Close()
	gz.Close()
	return buffer.String()
}

func newTestIngester(objects fakeObjects) (*fakeTestStore, *TestIngester) {
	results := &fakeTestStore{known: map[string]bool{}}
	return results, NewTestIngester(results, objects, slog.New(slog.DiscardHandler))
}

func TestIngestReadsReportsFromAnArchive(t *testing.T) {
	objects := fakeObjects{
		"raw/static-tests-1/static-tests.tar.gz": archive(t, map[string]string{
			"junit.xml":    junitXML,
			"phpstan.json": phpstanJSON,
			// Ni un rapport, ni un format connu : ignoré sans faire échouer le reste.
			"phpunit.log": "bruit",
		}),
	}
	results, ingester := newTestIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "static-tests-1"}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}

	tools := map[string]store.TestResult{}
	for _, result := range results.saved {
		tools[result.Tool] = result
	}
	if len(tools) != 2 {
		t.Fatalf("outils enregistrés = %v", tools)
	}
	if tools["phpunit"].Failed != 1 || tools["phpunit"].Status != "fail" {
		t.Errorf("phpunit = %+v", tools["phpunit"])
	}
	if tools["phpstan"].Family != "static" {
		t.Errorf("phpstan = %+v", tools["phpstan"])
	}
}

func TestIngestReadsAStandaloneFile(t *testing.T) {
	objects := fakeObjects{"raw/unit-1/junit.xml": junitXML}
	results, ingester := newTestIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 2, Name: "unit-1"}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	if len(results.saved) != 1 {
		t.Fatalf("%d rapport(s)", len(results.saved))
	}
	// Le lien vers le brut permet de rejouer l'ingestion depuis l'objet d'origine.
	if results.saved[0].RawObject != "raw/unit-1/junit.xml" {
		t.Errorf("objet source = %q", results.saved[0].RawObject)
	}
}

// L'observateur revoit un run terminé au redémarrage. Réenregistrer doublerait
// chaque cas, donc chaque comparaison à la release précédente.
func TestIngestIsIdempotentPerTool(t *testing.T) {
	objects := fakeObjects{"raw/unit-1/junit.xml": junitXML}
	results, ingester := newTestIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 2, Name: "unit-1"}); err != nil {
		t.Fatalf("première ingestion: %v", err)
	}
	results.known["phpunit"] = true

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 2, Name: "unit-1"}); err != nil {
		t.Fatalf("seconde ingestion: %v", err)
	}
	if len(results.saved) != 1 {
		t.Errorf("%d rapport(s), attendu 1", len(results.saved))
	}
}

// Les mesures de charge ne sont pas des cas de test : les faire passer ici les
// projetterait dans un modèle qui ne leur convient pas.
func TestLoadArtifactsAreLeftToTheirOwnIngester(t *testing.T) {
	objects := fakeObjects{"raw/load-test-1/k6/panier-summary.json": `{"metrics":{}}`}
	results, ingester := newTestIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 3, Name: "load-test-1"}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	if len(results.saved) != 0 {
		t.Errorf("%d rapport(s) enregistré(s)", len(results.saved))
	}
}

// Une campagne qui produit quatre rapports ne doit pas être perdue en entier
// parce que l'un d'eux est tronqué.
func TestOneBadReportDoesNotLoseTheOthers(t *testing.T) {
	objects := fakeObjects{
		"raw/static-tests-1/static-tests.tar.gz": archive(t, map[string]string{
			"junit.xml":    junitXML,
			"phpstan.json": `{"totals":`,
		}),
	}
	results, ingester := newTestIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 4, Name: "static-tests-1"}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	if len(results.saved) != 1 || results.saved[0].Tool != "phpunit" {
		t.Errorf("rapports = %+v", results.saved)
	}
}

// Une archive Cypress pèse surtout des vidéos : les rapatrier en mémoire pour
// les jeter aussitôt serait absurde.
func TestUnknownFilesAreNotLoaded(t *testing.T) {
	files, err := extract("x.tar.gz", bytes.NewReader([]byte(archive(t, map[string]string{
		"videos/panier.mp4": "beaucoup d'octets",
		"mochawesome.json":  `{"stats":{"duration":0},"results":[]}`,
	}))))
	if err != nil {
		t.Fatalf("extraction: %v", err)
	}
	if len(files) != 1 || files[0].name != "mochawesome.json" {
		t.Errorf("fichiers extraits = %+v", files)
	}
}

func TestRecognise(t *testing.T) {
	cases := map[string]string{
		"phpstan.json":          "phpstan",
		"reports/cs-fixer.json": "cs-fixer",
		"mochawesome.json":      "cypress",
		"junit-integration.xml": "phpunit-integration",
		"junit.xml":             "phpunit",
	}
	for name, expected := range cases {
		kind, known := recognise(name)
		if !known || kind.tool != expected {
			t.Errorf("recognise(%q) = %q/%v, attendu %q", name, kind.tool, known, expected)
		}
	}
	if _, known := recognise("videos/panier.mp4"); known {
		t.Error("une vidéo n'est pas un rapport")
	}
}

// Le défaut qui rendait la famille `integration` inobservable. Les Tasks écrivent
// sous `test-results/<famille>/` et nomment toutes leur rapport `junit.xml` :
// l'intégration tombait donc dans la règle des tests unitaires, et son échec
// était rapporté comme un échec unitaire — sur un signal qui pèse 25 au score au
// lieu de 20, et en laissant `integration` éternellement non mesurée.
func TestLeRepertoireNommeLaFamille(t *testing.T) {
	cases := map[string]struct {
		family results.Family
		tool   string
	}{
		"test-results/integration/junit.xml": {results.FamilyIntegration, "phpunit-integration"},
		"test-results/unit/junit.xml":        {results.FamilyUnit, "phpunit"},
		"test-results/phpstan/report.json":   {results.FamilyStatic, "phpstan"},
		"test-results/cs-fixer/report.json":  {results.FamilyStatic, "cs-fixer"},
		// Le même fichier, publié à plat : le nom prend le relais.
		"junit.xml": {results.FamilyUnit, "phpunit"},
	}

	for name, expected := range cases {
		kind, known := recognise(name)
		if !known {
			t.Errorf("recognise(%q) non reconnu", name)
			continue
		}
		if kind.family != expected.family || kind.tool != expected.tool {
			t.Errorf("recognise(%q) = %s/%s, attendu %s/%s",
				name, kind.family, kind.tool, expected.family, expected.tool)
		}
	}
}

// Deux rapports d'un même run, dans deux familles différentes, doivent être
// distingués. C'est le cas d'une archive qui porte à la fois l'unitaire et
// l'intégration — et le cas où l'ancienne reconnaissance les confondait.
func TestDeuxFamillesDansUneMemeArchiveNeSeConfondentPas(t *testing.T) {
	unit, _ := recognise("test-results/unit/junit.xml")
	integration, _ := recognise("test-results/integration/junit.xml")

	if unit.family == integration.family {
		t.Errorf("même famille %q pour deux répertoires distincts", unit.family)
	}
	// Les outils diffèrent aussi : l'idempotence de l'ingestion est indexée par
	// outil, et deux familles sous le même nom d'outil feraient perdre la seconde.
	if unit.tool == integration.tool {
		t.Errorf("même outil %q — la seconde ingestion serait ignorée", unit.tool)
	}
}

// Le contrat publié doit être vrai pour les six familles, pas pour quatre.
//
// C'est la raison d'être du lot F : « le normatif est le contrat, pas
// l'implémentation » (doc 12 §12.6). Les Tasks du MSE écrivent leur rapport
// Cypress sous `mocha/`, si bien que la règle de nom suffisait ; un projet qui
// suit la convention documentée et écrit `test-results/e2e/report.json`
// n'était **pas reconnu du tout**.
//
// Le silence est le pire des résultats : l'écran des tests reste vide, la
// famille `e2e` n'est jamais mesurée, et rien ne dit pourquoi. C'est exactement
// ce que doc 13 annonce comme le mode de défaillance à éviter.
func TestLeRepertoireDeFamillePubliéEstReconnu(t *testing.T) {
	cases := map[string]struct {
		family results.Family
		tool   string
	}{
		// Le nom du rapport ne dit rien : seul le répertoire le range.
		"test-results/e2e/report.json": {results.FamilyE2E, "cypress"},
		// La convention de l'outil reste lue : la retirer casserait les runs
		// existants du MSE.
		"test-results/mocha/mochawesome.json": {results.FamilyE2E, "cypress"},
	}

	for name, expected := range cases {
		kind, known := recognise(name)
		if !known {
			t.Errorf("recognise(%q) non reconnu — le contrat l'annonce pourtant", name)
			continue
		}
		if kind.family != expected.family || kind.tool != expected.tool {
			t.Errorf("recognise(%q) = %s/%s, attendu %s/%s",
				name, kind.family, kind.tool, expected.family, expected.tool)
		}
	}
}

// Pour l'analyse statique, le répertoire de famille ne suffit pas : PHPStan et
// php-cs-fixer produisent deux formats JSON différents, et le parseur ne se
// devine pas. Le nom du fichier prend alors le relais — c'est ce que le contrat
// doit exiger, plutôt que de laisser croire que `static/` suffit.
func TestSousStaticLOutilDoitResterIdentifiable(t *testing.T) {
	kind, known := recognise("test-results/static/phpstan.json")
	if !known || kind.family != results.FamilyStatic || kind.tool != "phpstan" {
		t.Errorf("recognise = %s/%s/%v — le nom doit désigner l'outil",
			kind.family, kind.tool, known)
	}

	// Sans outil identifiable, rien n'est deviné : mieux vaut ignorer que
	// choisir un parseur au hasard et rendre des violations fausses.
	if _, known := recognise("test-results/static/report.json"); known {
		t.Error("un rapport statique sans outil identifiable ne doit pas être deviné")
	}
}
