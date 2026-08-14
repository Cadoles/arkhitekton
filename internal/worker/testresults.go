package worker

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/results"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

// maxArtifactBytes borne ce qu'un rapport peut peser une fois décompressé.
//
// Une archive publiée par un pipeline est produite dans le cluster et n'est pas
// hostile, mais un rapport JUnit de plusieurs gigaoctets — une boucle de test
// partie en vrille — mettrait le worker à genoux au lieu de le signaler. La
// borne transforme une panne mémoire en message.
const maxArtifactBytes = 64 << 20

// TestResultStore est ce que l'ingesteur attend de la base.
type TestResultStore interface {
	SaveTestResult(ctx context.Context, result store.TestResult, cases []store.TestCase) (int64, error)
	TestResultExists(ctx context.Context, runID int64, tool string) (bool, error)
}

// TestIngester enregistre les rapports de tests publiés par un run.
type TestIngester struct {
	store   TestResultStore
	objects Objects
	logger  *slog.Logger
}

func NewTestIngester(results TestResultStore, objects Objects, logger *slog.Logger) *TestIngester {
	return &TestIngester{store: results, objects: objects, logger: logger}
}

// artifactKind associe un nom de fichier à son lecteur.
//
// La reconnaissance se fait sur le **nom**, décidé par le catalogue et non par le
// pipeline : c'est ce qui permet d'ajouter un outil sans toucher au code
// d'ingestion, et ce qui évite de deviner un format à partir de son contenu.
type artifactKind struct {
	family results.Family
	tool   string
	parse  func(raw []byte) (results.Report, error)
}

// recognise identifie un fichier d'artefact.
//
// Le nom du fichier suffit parce que les pipelines les produisent sous des noms
// fixes (P3, doc 10). Un fichier inconnu est ignoré en silence : une archive
// contient aussi des journaux et des captures, et échouer dessus perdrait les
// rapports qui l'accompagnent.
func recognise(name string) (artifactKind, bool) {
	base := strings.ToLower(path.Base(name))

	switch {
	case strings.Contains(base, "phpstan") && strings.HasSuffix(base, ".json"):
		return artifactKind{results.FamilyStatic, "phpstan", results.ParsePHPStan}, true

	case (strings.Contains(base, "cs-fixer") || strings.Contains(base, "csfixer")) &&
		strings.HasSuffix(base, ".json"):
		return artifactKind{results.FamilyStatic, "cs-fixer", results.ParseCSFixer}, true

	case strings.Contains(base, "mocha") && strings.HasSuffix(base, ".json"):
		return artifactKind{results.FamilyE2E, "cypress", results.ParseMochawesome}, true

	case strings.Contains(base, "integration") && strings.HasSuffix(base, ".xml"):
		return artifactKind{results.FamilyIntegration, "phpunit-integration", func(raw []byte) (results.Report, error) {
			return results.ParseJUnit(results.FamilyIntegration, "phpunit-integration", raw)
		}}, true

	case strings.HasSuffix(base, ".xml") && (strings.Contains(base, "junit") || strings.Contains(base, "phpunit")):
		return artifactKind{results.FamilyUnit, "phpunit", func(raw []byte) (results.Report, error) {
			return results.ParseJUnit(results.FamilyUnit, "phpunit", raw)
		}}, true
	}

	return artifactKind{}, false
}

// Ingest lit les artefacts d'un run terminé et enregistre ce qu'il y reconnaît.
//
// Idempotent par outil : l'observateur revoit un run terminé au redémarrage, et
// réenregistrer doublerait chaque cas — donc chaque comparaison à la release
// précédente.
func (i *TestIngester) Ingest(ctx context.Context, run LoadRun) error {
	prefix := "raw/" + run.Name + "/"
	objects, err := i.objects.List(ctx, prefix)
	if err != nil {
		return fmt.Errorf("artefacts du run %q: %w", run.Name, err)
	}

	var failures []string
	for _, object := range objects {
		// Les campagnes de charge ont leur propre ingesteur : leurs mesures ne
		// sont pas des cas de test, et les faire passer ici les projetterait dans
		// un modèle qui ne leur convient pas.
		if strings.Contains(object.Key, "/k6/") {
			continue
		}

		if err := i.ingestObject(ctx, run, object.Key); err != nil {
			i.logger.Error("artefact de tests", "run", run.Name, "objet", object.Key, "error", err)
			failures = append(failures, object.Key)
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("run %q: %d artefact(s) non enregistré(s): %s",
			run.Name, len(failures), strings.Join(failures, ", "))
	}
	return nil
}

func (i *TestIngester) ingestObject(ctx context.Context, run LoadRun, key string) error {
	content, err := i.objects.Get(ctx, key)
	if err != nil {
		return err
	}
	defer content.Close()

	files, err := extract(key, content)
	if err != nil {
		return err
	}

	for _, file := range files {
		kind, known := recognise(file.name)
		if !known {
			continue
		}

		already, err := i.store.TestResultExists(ctx, run.ID, kind.tool)
		if err != nil {
			return err
		}
		if already {
			continue
		}

		report, err := kind.parse(file.content)
		if err != nil {
			// Un rapport illisible n'empêche pas d'enregistrer les autres : une
			// campagne qui produit quatre rapports ne doit pas être perdue en
			// entier parce que l'un d'eux est tronqué.
			i.logger.Error("rapport illisible",
				"run", run.Name, "fichier", file.name, "error", err)
			continue
		}

		cases := make([]store.TestCase, 0, len(report.Cases))
		for _, testCase := range report.Cases {
			cases = append(cases, store.TestCase{
				Suite: testCase.Suite, Name: testCase.Name, Status: string(testCase.Status),
				DurationMS:     testCase.DurationMS,
				FailureMessage: testCase.Message, FailureTrace: testCase.Trace,
			})
		}

		summary := report.Summary
		if summary == "" {
			summary = results.SummarizeJUnit(report)
		}

		if _, err := i.store.SaveTestResult(ctx, store.TestResult{
			RunID: run.ID, ReleaseID: run.ReleaseID,
			Family: string(kind.family), Tool: kind.tool, Status: report.Status,
			Total: report.Total, Passed: report.Passed, Failed: report.Failed,
			Skipped: report.Skipped, Flaky: report.Flaky,
			DurationMS: report.DurationMS, Summary: summary, RawObject: key,
		}, cases); err != nil {
			return err
		}

		i.logger.Info("rapport de tests enregistré",
			"run", run.Name, "outil", kind.tool, "cas", len(cases), "statut", report.Status)
	}

	return nil
}

// artifactFile est un fichier extrait d'un objet.
type artifactFile struct {
	name    string
	content []byte
}

// extract rend les fichiers d'un objet.
//
// Une archive est déballée, un fichier isolé est rendu tel quel. Les deux formes
// coexistent parce que les pipelines publient les deux : un rapport JUnit dont le
// nom est connu part seul, un répertoire Cypress part en `tar.gz` — ses noms de
// fichiers dépendent des specs exécutées et ne peuvent pas être pré-signés un par
// un (P1).
func extract(key string, source io.Reader) ([]artifactFile, error) {
	if !strings.HasSuffix(key, ".tar.gz") && !strings.HasSuffix(key, ".tgz") {
		content, err := io.ReadAll(io.LimitReader(source, maxArtifactBytes))
		if err != nil {
			return nil, err
		}
		return []artifactFile{{name: key, content: content}}, nil
	}

	gz, err := gzip.NewReader(source)
	if err != nil {
		return nil, fmt.Errorf("archive %q illisible: %w", key, err)
	}
	defer gz.Close()

	var files []artifactFile
	reader := tar.NewReader(gz)
	var budget int64 = maxArtifactBytes

	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("archive %q illisible: %w", key, err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}

		// Seuls les formats qu'on sait lire sont chargés en mémoire : une archive
		// Cypress pèse surtout des vidéos, qu'il serait absurde de rapatrier pour
		// les jeter aussitôt.
		if _, known := recognise(header.Name); !known {
			continue
		}

		content, err := io.ReadAll(io.LimitReader(reader, budget))
		if err != nil {
			return nil, err
		}
		budget -= int64(len(content))
		if budget <= 0 {
			return nil, fmt.Errorf("archive %q: taille décompressée au-delà de la limite", key)
		}

		files = append(files, artifactFile{name: header.Name, content: content})
	}

	return files, nil
}

// readLimited lit un flux en bornant ce qu'il peut occuper en mémoire.
func readLimited(source io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(source, maxArtifactBytes))
}
