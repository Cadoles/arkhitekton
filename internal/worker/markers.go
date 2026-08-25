package worker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/logs"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/results"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Raccordement du canal de marqueurs.
 *
 * Les Tasks émettent un descripteur par outil sur leur sortie standard —
 * `##META## {"tool":"phpstan","status":"ok"}` — et `internal/results` sait le
 * lire depuis longtemps. Personne ne l'appelait : la fonction était écrite,
 * testée, et sans le moindre appelant (doc 13 §13.9).
 *
 * Ce que l'absence coûtait : **un outil planté ne se distinguait pas d'un outil
 * qui n'avait rien trouvé**. Les deux publient zéro violation, l'un parce que le
 * code est sain, l'autre parce que le scanner est mort avant de commencer. Sans
 * le descripteur, le second s'affichait « conforme ». C'est exactement la
 * propriété que le canal existait pour préserver, et elle était perdue.
 *
 * Le descripteur est produit par le pipeline, **jamais déduit ici**. Lui seul
 * peut faire la différence, et un dashboard qui la devinerait aurait tort au
 * premier outil qui sort en 1 pour dire « j'ai trouvé quelque chose ».
 */

// maxMarkerScanBytes borne ce qu'on lit d'un journal de task pour y chercher des
// marqueurs.
//
// Un descripteur pèse une centaine d'octets et peut se trouver n'importe où dans
// le flux — chaque outil émet le sien dès qu'il a fini. Il faut donc parcourir,
// et la borne évite qu'un journal parti en vrille mette le worker à genoux. Son
// franchissement est signalé : un marqueur manqué se lit comme un marqueur
// absent, et ne pas le dire ferait passer une lecture tronquée pour un pipeline
// muet.
const maxMarkerScanBytes = 8 << 20

// LogSource est ce que l'ingesteur attend du lecteur de journaux.
type LogSource interface {
	From(ctx context.Context, runName, taskName string, offset, length int64) (logs.Segment, error)
}

// MarkerStore est ce que l'ingesteur attend de la base.
type MarkerStore interface {
	SaveTestResult(ctx context.Context, result store.TestResult, cases []store.TestCase) (int64, error)
	TestResultExists(ctx context.Context, runID int64, tool string) (bool, error)
}

// MarkerIngester enregistre ce que les descripteurs d'un run disent des outils
// qui n'ont pas conclu.
type MarkerIngester struct {
	store   MarkerStore
	source  LogSource
	markers results.MarkerSet
	logger  *slog.Logger
}

func NewMarkerIngester(
	db MarkerStore, source LogSource, markers results.MarkerSet, logger *slog.Logger,
) *MarkerIngester {
	return &MarkerIngester{store: db, source: source, markers: markers, logger: logger}
}

// MarkerRun est le run à examiner.
type MarkerRun struct {
	ID        int64
	Name      string
	Tasks     []string
	ReleaseID *int64
	// Family est la famille de signal que le pipeline mesure, telle que le
	// catalogue la déclare. Vide, rien n'est enregistré : un résultat sans famille
	// ne se rattache à aucun signal et n'apparaîtrait nulle part.
	Family string
}

// Ingest lit les journaux d'un run terminé et enregistre les outils qui n'ont pas
// conclu.
//
// **Seuls** ceux-là. Un outil qui a fait son travail publie un artefact, et c'est
// lui qui porte le décompte : en tirer un second résultat depuis le descripteur
// donnerait deux lignes pour une exécution, sans que rien ne dise laquelle fait
// foi.
//
// Appelé **avant** l'ingestion des artefacts, et l'ordre porte une décision : les
// deux sont idempotents par outil, donc le premier arrivé l'emporte. Le
// descripteur doit gagner — il est le seul à savoir que l'outil a échoué, là où un
// artefact partiel se lirait comme un résultat complet.
func (i *MarkerIngester) Ingest(ctx context.Context, run MarkerRun) error {
	if run.Family == "" || len(run.Tasks) == 0 {
		return nil
	}

	var failures []string
	for _, task := range run.Tasks {
		text, err := i.readTask(ctx, run.Name, task)
		if err != nil {
			// Une task sans journal est un cas normal : elle a pu être sautée, ou
			// son collecteur n'a rien scellé. Ce n'est pas une raison de perdre
			// les marqueurs des autres.
			i.logger.Debug("journal illisible pour la lecture des marqueurs",
				"run", run.Name, "task", task, "error", err)
			continue
		}

		for tool, meta := range i.markers.ScanMarkers(text).Metas {
			if meta.Ran() {
				continue
			}
			if err := i.record(ctx, run, tool, meta); err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", tool, err))
			}
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("marqueurs du run %q: %s", run.Name, strings.Join(failures, "; "))
	}
	return nil
}

// record enregistre un outil qui n'a pas conclu.
func (i *MarkerIngester) record(
	ctx context.Context, run MarkerRun, tool string, meta results.Meta,
) error {
	exists, err := i.store.TestResultExists(ctx, run.ID, tool)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	// `na` et non `fail`, et la nuance est tout l'objet du canal : « échec » se
	// lit comme un verdict sur le code, alors qu'ici c'est l'outil qui n'a pas
	// rendu de verdict. Le score sort ce signal du calcul et redistribue son
	// poids, au lieu de compter un zéro qui serait faux (B11).
	result := store.TestResult{
		RunID:     run.ID,
		ReleaseID: run.ReleaseID,
		Family:    run.Family,
		Tool:      tool,
		Status:    results.ReportNA,
		Summary:   markerSummary(meta),
	}

	if _, err := i.store.SaveTestResult(ctx, result, nil); err != nil {
		return err
	}
	i.logger.Warn("outil sans conclusion",
		"run", run.Name, "outil", tool, "statut", meta.Status,
		"code_sortie", meta.ExitCode, "detail", meta.Error)
	return nil
}

// markerSummary compose la ligne affichée pour un outil qui n'a pas conclu.
//
// Le message du pipeline quand il y en a un, sinon une phrase qui dit au moins
// pourquoi la ligne existe. Une cellule vide se lirait comme une absence de
// mesure sans cause, ce qui est la moitié de l'information.
func markerSummary(meta results.Meta) string {
	if detail := strings.TrimSpace(meta.Error); detail != "" {
		return detail
	}
	if status := strings.TrimSpace(meta.Status); status != "" {
		return fmt.Sprintf("l'outil n'a pas conclu (statut %q, code %d)", status, meta.ExitCode)
	}
	return fmt.Sprintf("l'outil n'a pas conclu (code %d)", meta.ExitCode)
}

// readTask lit le journal d'une task, borné.
//
// Le journal est lu par tranches et non d'un bloc : c'est l'interface du lecteur,
// qui sert d'abord le suivi en direct. La boucle s'arrête sur la fin du journal
// ou sur la borne, selon ce qui vient en premier.
func (i *MarkerIngester) readTask(ctx context.Context, runName, taskName string) (string, error) {
	var builder strings.Builder
	var offset int64

	for {
		segment, err := i.source.From(ctx, runName, taskName, offset, 0)
		if err != nil {
			return "", err
		}
		builder.Write(segment.Content)

		if segment.NextOffset >= segment.Size || segment.NextOffset <= offset {
			return builder.String(), nil
		}
		offset = segment.NextOffset

		if int64(builder.Len()) >= maxMarkerScanBytes {
			i.logger.Warn("journal tronqué pour la lecture des marqueurs",
				"run", runName, "task", taskName,
				"lu", builder.Len(), "taille", segment.Size)
			return builder.String(), nil
		}
	}
}

// markerRunFor compose ce qu'il faut savoir d'un run pour y chercher des
// marqueurs.
//
// Rend `false` quand le catalogue ne dit pas quelle famille le pipeline mesure :
// sans elle, un outil sans artefact ne se rattache à aucun signal.
func markerRunFor(
	pipelines *catalog.Catalog, kind string, run MarkerRun,
) (MarkerRun, bool) {
	if pipelines == nil {
		return MarkerRun{}, false
	}
	pipeline, found := pipelines.Find(kind)
	if !found {
		return MarkerRun{}, false
	}
	run.Family = pipeline.Family()
	return run, run.Family != ""
}
