package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/bench"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/blob"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

// LoadStore est ce que l'ingesteur attend de la base.
type LoadStore interface {
	SaveLoadResult(ctx context.Context, result store.LoadResult, measures []store.BenchMeasure) (int64, error)
	StartConditions(ctx context.Context, jobID int64) (bool, int, error)
	LoadResultExists(ctx context.Context, runID int64, scenario string) (bool, error)
}

// Objects est ce que l'ingesteur attend du stockage.
type Objects interface {
	List(ctx context.Context, prefix string) ([]ObjectRef, error)
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// ObjectRef est le strict nécessaire pour retrouver un résumé.
type ObjectRef struct {
	Key  string
	Size int64
}

// LoadIngester enregistre les campagnes de charge produites par un run.
//
// L'ingestion est déclenchée par la **fin du run**, et non par une notification
// du runner : celui-ci vit hors du cluster, publie sur MinIO avec une URL
// pré-signée fournie par le dashboard, et n'a aucun credential ni aucun chemin de
// retour vers le dashboard ([B16], [B14]). Le seul signal fiable est donc celui
// que le dashboard possède déjà — l'observateur Tekton constate la fin du
// PipelineRun qui pilotait le runner.
type LoadIngester struct {
	store   LoadStore
	objects Objects
	metrics []bench.Metric
	logger  *slog.Logger
}

func NewLoadIngester(releases LoadStore, objects Objects, logger *slog.Logger) *LoadIngester {
	return &LoadIngester{store: releases, objects: objects, metrics: bench.DefaultMetrics, logger: logger}
}

// Ingest lit les résumés k6 d'un run terminé et les enregistre.
//
// Idempotent : l'observateur peut revoir un run terminé — au redémarrage, ou
// parce qu'un informer resynchronise son cache. Réenregistrer produirait des
// points d'historique en double, c'est-à-dire une tendance fausse.
func (i *LoadIngester) Ingest(ctx context.Context, run LoadRun) error {
	prefix := "raw/" + run.Name + "/k6/"
	objects, err := i.objects.List(ctx, prefix)
	if err != nil {
		return fmt.Errorf("résumés du run %q: %w", run.Name, err)
	}
	if len(objects) == 0 {
		// Un run de charge sans résumé n'est pas une anomalie de l'ingesteur : le
		// runner a pu échouer avant de publier, et le run porte déjà son échec.
		i.logger.Info("aucun résumé de charge", "run", run.Name)
		return nil
	}

	exclusive, concurrent := false, 0
	if run.JobID != 0 {
		exclusive, concurrent, err = i.store.StartConditions(ctx, run.JobID)
		if err != nil && !errors.Is(err, store.ErrJobNotFound) {
			return err
		}
	}

	var failures []string
	for _, object := range objects {
		if !strings.HasSuffix(object.Key, ".json") {
			continue
		}
		if err := i.ingestOne(ctx, run, object, exclusive, concurrent); err != nil {
			// Un résumé illisible n'empêche pas d'enregistrer les autres : une
			// campagne à quatre scénarios ne doit pas être perdue en entier parce
			// que l'un d'eux a produit un fichier tronqué.
			i.logger.Error("résumé de charge", "run", run.Name, "objet", object.Key, "error", err)
			failures = append(failures, object.Key)
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("run %q: %d résumé(s) non enregistré(s): %s",
			run.Name, len(failures), strings.Join(failures, ", "))
	}
	return nil
}

func (i *LoadIngester) ingestOne(
	ctx context.Context, run LoadRun, object ObjectRef, exclusive bool, concurrent int,
) error {
	scenario := bench.ScenarioFromKey(object.Key)

	known, err := i.store.LoadResultExists(ctx, run.ID, scenario)
	if err != nil {
		return err
	}
	if known {
		return nil
	}

	content, err := i.objects.Get(ctx, object.Key)
	if err != nil {
		return err
	}
	defer content.Close()

	raw, err := io.ReadAll(content)
	if err != nil {
		return err
	}

	summary, err := bench.ParseSummary(scenario, raw, i.metrics)
	if err != nil {
		return err
	}

	measures := make([]store.BenchMeasure, 0, len(summary.Measures))
	for _, measure := range summary.Measures {
		measures = append(measures, store.BenchMeasure{
			Metric: measure.Metric, Label: measure.Label, Unit: measure.Unit,
			Value: measure.Value, LowerBetter: measure.LowerBetter,
			Volume: measure.Volume, ThresholdOK: measure.ThresholdOK,
		})
	}

	_, err = i.store.SaveLoadResult(ctx, store.LoadResult{
		RunID: run.ID, ReleaseID: run.ReleaseID, Scenario: scenario,
		// Les conditions **constatées**, relevées à la réservation du job. Pas
		// celles demandées : la barrière draine avant de lancer, mais c'est le
		// constat qui décide si la mesure entre dans la comparaison (B16).
		Exclusive: exclusive, ConcurrentJobs: concurrent,
		DurationMS: run.DurationMS,
		Summary:    summarise(summary),
		RawObject:  object.Key,
	}, measures)
	if err != nil {
		return err
	}

	i.logger.Info("campagne de charge enregistrée",
		"run", run.Name, "scénario", scenario, "mesures", len(measures),
		"exclusive", exclusive, "concurrents", concurrent)
	return nil
}

// LoadRun est le run dont on ingère les résultats.
type LoadRun struct {
	ID         int64
	Name       string
	JobID      int64
	ReleaseID  *int64
	DurationMS int64
}

// summarise rend un résumé d'une ligne, affiché sans avoir à ouvrir les mesures.
func summarise(summary bench.Summary) string {
	parts := []string{fmt.Sprintf("%d mesure(s)", len(summary.Measures))}
	if summary.VUs > 0 {
		parts = append(parts, fmt.Sprintf("%d VU au pic", summary.VUs))
	}
	if len(summary.ThresholdsFailed) > 0 {
		// Nommé et non compté : savoir *quel* seuil a lâché est ce qui décide de
		// la suite.
		parts = append(parts, "seuils non tenus : "+strings.Join(summary.ThresholdsFailed, ", "))
	}
	return strings.Join(parts, ", ")
}

// objectStore adapte le client MinIO à ce que l'ingesteur attend.
//
// L'adaptateur existe pour que l'ingesteur reste testable sans MinIO : ce qu'il
// lui faut tient en deux méthodes, et les exiger sur le client complet
// obligerait à en simuler douze.
type objectStore struct {
	client interface {
		List(ctx context.Context, prefix string) ([]blob.ObjectInfo, error)
		Get(ctx context.Context, key string) (io.ReadCloser, error)
	}
}

// NewObjectStore rend l'adaptateur consommé par l'ingesteur.
func NewObjectStore(client *blob.Client) Objects { return objectStore{client: client} }

func (o objectStore) List(ctx context.Context, prefix string) ([]ObjectRef, error) {
	objects, err := o.client.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	refs := make([]ObjectRef, 0, len(objects))
	for _, object := range objects {
		refs = append(refs, ObjectRef{Key: object.Key, Size: object.Size})
	}
	return refs, nil
}

func (o objectStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return o.client.Get(ctx, key)
}
