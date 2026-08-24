package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/blob"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/tekton"
)

// tickInterval borne l'attente entre deux tours de boucle.
//
// La doc prévoit un réveil par `LISTEN/NOTIFY` avec un tick de sécurité. Seul le
// tick est implémenté pour l'instant : deux secondes de latence au lancement sont
// sans conséquence pour un pipeline qui dure des minutes, alors qu'une notification
// perdue laisserait la file immobile jusqu'au tick suivant de toute façon. La
// notification reste un raffinement, pas un prérequis.
const tickInterval = 2 * time.Second

// Queue est ce que l'ordonnanceur attend de la base.
type Queue interface {
	ClaimNext(ctx context.Context, limits store.Limits, runName func(kind string, id int64) string) (store.Job, error)
	Requeue(ctx context.Context, id int64, reason string) (bool, error)
	FinishJob(ctx context.Context, id int64, state, reason string) error
	ExplainQueue(ctx context.Context, limits store.Limits) error
	PendingCancellations(ctx context.Context) ([]store.RunningJob, error)
	// QueueLimits rend les plafonds réglés à la main. Relus à chaque tour : un
	// réglage doit prendre effet sans redémarrer le worker, sinon personne ne
	// s'en sert.
	QueueLimits(ctx context.Context) (map[string]int, error)
}

// LocalTask exécute un job dans le processus, sans passer par Tekton.
//
// Tout ce que la file porte ne devient pas un PipelineRun. Générer un bon de
// livraison lit la base et écrit un objet : le faire traverser Tekton coûterait
// un pod et un workspace pour produire deux pages. Passer malgré tout par la
// file donne l'ordonnancement, la trace et la visibilité — un export apparaît
// dans la file comme le reste, et son échec se lit au même endroit.
type LocalTask interface {
	// Kind est le `kind` traité. Ces kinds sont absents du catalogue, qui décrit
	// ce qui devient un PipelineRun.
	Kind() string
	Run(ctx context.Context, job store.Job) error
}

// Presigner mint les URLs d'écriture des artefacts.
//
// Le layout appartient au dashboard : il calcule la clé exacte de chaque artefact
// **avant** de créer le run, et fournit l'URL correspondante en paramètre. Le
// pipeline n'a donc ni bucket, ni préfixe, ni identifiants — et le lecteur n'a
// rien à chercher par préfixe ensuite (B14).
type Presigner interface {
	PresignPut(ctx context.Context, key string, ttl time.Duration) (*url.URL, error)
}

// uploadTTL borne la validité d'une URL d'écriture.
//
// Elle doit survivre au pipeline le plus long — une campagne E2E dépasse l'heure —
// sans devenir un droit d'écriture durable. Six heures : au-delà, une URL fuitée
// resterait exploitable après la fin du run qui la justifiait.
const uploadTTL = 6 * time.Hour

// Scheduler transforme les jobs en file en PipelineRun.
type Scheduler struct {
	queue   Queue
	cluster *tekton.Client
	catalog *catalog.Catalog
	config  map[string]string
	local   map[string]LocalTask
	// uploads mint les URLs pré-signées. Nil quand le stockage n'est pas
	// disponible : les runs partent alors sans publication, et le disent.
	uploads Presigner
	keys    blob.Keys
	// instance identifie la base. Entre dans le nom des runs pour qu'une remise à
	// zéro ne produise pas d'homonymes de PipelineRun restés dans le cluster.
	instance string
	logger   *slog.Logger
}

// WithInstance déclare l'identité de la base.
//
// Séparée du constructeur pour ne pas propager une lecture en base dans tous les
// appelants : elle est facultative, et son absence retombe sur l'ancien nommage —
// suffisant pour un test, jamais pour un déploiement qui a déjà tourné.
func (s *Scheduler) WithInstance(id string) *Scheduler {
	s.instance = id
	return s
}

// WithUploads branche la génération des URLs d'artefacts.
func (s *Scheduler) WithUploads(presigner Presigner) *Scheduler {
	s.uploads = presigner
	return s
}

// NewSchedulerFromCatalog assemble l'ordonnanceur. `config` fournit les valeurs
// des paramètres de source `config`.
func NewSchedulerFromCatalog(
	queue Queue, cluster *tekton.Client, pipelines *catalog.Catalog,
	config map[string]string, logger *slog.Logger,
) *Scheduler {
	return &Scheduler{
		queue: queue, cluster: cluster, catalog: pipelines,
		config: config, local: map[string]LocalTask{}, logger: logger,
	}
}

// Handle enregistre une tâche exécutée dans le processus.
func (s *Scheduler) Handle(task LocalTask) {
	s.local[task.Kind()] = task
}

// Run fait tourner la boucle jusqu'à annulation.
//
// Une seule instance doit l'exécuter : c'est ce que garantit le bail. Le
// `skip locked` de la réservation couvre le reste — un double lancement de
// déploiement en production n'est pas rattrapable.
func (s *Scheduler) Run(ctx context.Context) error {
	s.logger.Info("ordonnanceur démarré",
		"plafond", s.catalog.MaxConcurrentRuns, "kinds", s.catalog.Kinds())

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	for {
		// Les plafonds sont relus une fois par tour, et non par job : un réglage
		// changé pendant qu'un tour vide la file s'appliquera au tour suivant, ce
		// qui suffit largement, alors qu'une lecture par job multiplierait les
		// allers-retours en base pour une valeur qui bouge une fois par mois.
		limits := s.limits(ctx)

		// Chaque tour vide la file autant que les règles le permettent : servir
		// un seul job par tick ferait démarrer quatre jobs éligibles en huit
		// secondes, sans raison.
		for s.dispatchOne(ctx, limits) {
			if ctx.Err() != nil {
				return nil
			}
		}

		s.applyCancellations(ctx)

		if err := s.queue.ExplainQueue(ctx, limits); err != nil {
			s.logger.Error("explication de la file", "error", err)
		}

		select {
		case <-ctx.Done():
			s.logger.Info("arrêt de l'ordonnanceur")
			return nil
		case <-ticker.C:
		}
	}
}

// limits compose les plafonds effectifs : ceux du catalogue, écrasés par les
// réglages enregistrés.
//
// Un échec de lecture ne fait pas s'arrêter la file : les plafonds du catalogue
// s'appliquent alors seuls. Se figer parce qu'un réglage n'a pas pu être lu
// coûterait plus que de tourner un tour avec les valeurs par défaut.
func (s *Scheduler) limits(ctx context.Context) store.Limits {
	limits := store.Limits{
		PerKind: s.catalog.Concurrency(),
		Default: catalog.DefaultKindConcurrency,
		Global:  s.catalog.MaxConcurrentRuns,
	}

	overrides, err := s.queue.QueueLimits(ctx)
	if err != nil {
		s.logger.Error("lecture des plafonds de file", "error", err)
		return limits
	}
	for kind, limit := range overrides {
		limits.PerKind[kind] = limit
	}
	return limits
}

// dispatchOne réserve et lance un job. Rend false quand il n'y a plus rien à
// faire pour ce tour.
func (s *Scheduler) dispatchOne(ctx context.Context, limits store.Limits) bool {
	job, err := s.queue.ClaimNext(ctx, limits,
		func(kind string, id int64) string { return RunName(s.instance, kind, id) })
	if err != nil {
		if !errors.Is(err, store.ErrJobNotFound) {
			s.logger.Error("réservation d'un job", "error", err)
		}
		return false
	}

	// Les tâches locales sont consultées avant le catalogue : leur `kind` n'y
	// figure pas, et l'absence du catalogue conclurait le job en échec.
	if task, local := s.local[job.Kind]; local {
		s.runLocal(ctx, task, job)
		return true
	}

	pipeline, found := s.catalog.Find(job.Kind)
	if !found {
		// Le catalogue a changé entre la mise en file et le lancement. Remettre
		// le job en file le ferait boucler : mieux vaut le conclure en échec avec
		// un motif qui nomme la cause.
		s.logger.Error("kind absent du catalogue", "job", job.ID, "kind", job.Kind)
		s.retry(ctx, job.ID, fmt.Sprintf("kind %q absent du catalogue", job.Kind))
		return false
	}

	params, err := s.resolveParams(pipeline, job.Params)
	if err == nil {
		err = s.addUploadURLs(ctx, pipeline, job.RunName, params)
	}
	// Les noms de secrets sont résolus dans la même chaîne que les paramètres :
	// ce sont deux façons pour le catalogue de désigner une valeur de
	// configuration, et les traiter ensemble évite qu'un run parte avec les uns
	// résolus et les autres non.
	resolved := pipeline
	if err == nil {
		resolved, err = pipeline.ResolveSecrets(s.config)
	}
	if err != nil {
		s.logger.Error("paramètres du job", "job", job.ID, "error", err)
		s.retry(ctx, job.ID, err.Error())
		return false
	}

	// La création vient **après** la transition validée en base. Si elle échoue,
	// le job repasse `queued` avec un essai de plus ; l'ordre inverse produirait
	// un run que le dashboard ne saurait pas rattacher.
	if _, err := s.cluster.Create(ctx, tekton.Request{
		Name:     job.RunName,
		JobID:    job.ID,
		Instance: s.instance,
		Pipeline: resolved,
		Params:   params,
	}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Le run existe déjà sous ce nom : une tentative précédente a abouti
			// dans le cluster sans que le worker l'apprenne — mort entre la
			// création et sa prise en compte. Le remettre en file en créerait un
			// second, ou boucherait la file indéfiniment. L'état visé est
			// atteint : on continue.
			s.logger.Warn("run déjà présent, tentative précédente aboutie",
				"job", job.ID, "run", job.RunName)
			return true
		}

		s.logger.Error("création du PipelineRun", "job", job.ID, "run", job.RunName, "error", err)
		s.retry(ctx, job.ID, err.Error())
		return false
	}

	s.logger.Info("run lancé",
		"job", job.ID, "kind", job.Kind, "run", job.RunName, "file", limits.Of(job.Kind))
	return true
}

// addUploadURLs ajoute une URL pré-signée par artefact déclaré.
//
// Faite ici, avant la création du run, et non par le pipeline : c'est ce qui
// permet au dashboard de connaître la clé de chaque artefact sans la chercher, et
// ce qui sort le secret MinIO des Tasks de test.
//
// Un artefact non déclaré ne reçoit pas d'URL, et sa publication est simplement
// ignorée par la Task. C'est la dégradation voulue : un pipeline qui produit plus
// que ce que le catalogue annonce ne doit pas échouer pour autant.
func (s *Scheduler) addUploadURLs(
	ctx context.Context, pipeline catalog.Pipeline, runName string, params map[string]string,
) error {
	if len(pipeline.Uploads) == 0 {
		return nil
	}
	if s.uploads == nil {
		// Sans stockage, le run part sans publication. Le refuser priverait
		// l'utilisateur d'une exécution qui reste utile — les journaux, eux,
		// passent par un autre chemin.
		s.logger.Warn("stockage indisponible, artefacts non publiés", "run", runName)
		return nil
	}

	for name, upload := range pipeline.Uploads {
		if upload.Param == "" || upload.Artifact == "" {
			return fmt.Errorf("artefact %q: `param` et `artifact` sont requis pour minter une URL", name)
		}

		key, err := s.keys.RawArtifact(runName, upload.Artifact)
		if err != nil {
			return fmt.Errorf("artefact %q: %w", name, err)
		}

		signed, err := s.uploads.PresignPut(ctx, key, uploadTTL)
		if err != nil {
			return fmt.Errorf("artefact %q: %w", name, err)
		}
		params[upload.Param] = signed.String()
	}
	return nil
}

// runLocal exécute une tâche du processus et conclut son job.
//
// La conclusion est immédiate et non déléguée à l'informer : aucun PipelineRun
// n'existe pour ce job, donc rien d'autre ne viendra le terminer. Un job local
// laissé `running` bloquerait sa classe indéfiniment.
func (s *Scheduler) runLocal(ctx context.Context, task LocalTask, job store.Job) {
	if err := task.Run(ctx, job); err != nil {
		s.logger.Error("tâche locale", "job", job.ID, "kind", job.Kind, "error", err)
		// Échec définitif plutôt que remise en file : ces tâches échouent sur des
		// causes qui ne se corrigent pas d'elles-mêmes — une release sans image,
		// un format inconnu. Réessayer produirait la même erreur en boucle.
		if err := s.queue.FinishJob(ctx, job.ID, store.JobFailed, err.Error()); err != nil {
			s.logger.Error("conclusion du job", "job", job.ID, "error", err)
		}
		return
	}

	if err := s.queue.FinishJob(ctx, job.ID, store.JobSucceeded, ""); err != nil {
		s.logger.Error("conclusion du job", "job", job.ID, "error", err)
	}
	s.logger.Info("tâche locale terminée", "job", job.ID, "kind", job.Kind)
}

// applyCancellations exécute les demandes d'arrêt enregistrées par l'api.
//
// Le patch Tekton est idempotent : le rejouer sur un run déjà annulé ne coûte
// qu'un appel. C'est ce qui permet de ne pas tenir d'état supplémentaire pour
// savoir si la demande a été transmise — l'informer finira par constater la fin,
// et la demande disparaîtra avec le job.
func (s *Scheduler) applyCancellations(ctx context.Context) {
	pending, err := s.queue.PendingCancellations(ctx)
	if err != nil {
		s.logger.Error("lecture des annulations demandées", "error", err)
		return
	}

	for _, job := range pending {
		if err := s.cluster.Cancel(ctx, job.RunName); err != nil {
			s.logger.Error("annulation dans le cluster",
				"job", job.ID, "run", job.RunName, "error", err)
			continue
		}
		s.logger.Info("annulation transmise au cluster", "job", job.ID, "run", job.RunName)
	}
}

// resolveParams compose les paramètres du run à partir du catalogue et de la
// demande.
//
// Les trois sources ne se mélangent pas : `input` vient de l'utilisateur,
// `config` de la configuration du dashboard, `literal` du catalogue. Laisser une
// demande fournir un paramètre de source `config` permettrait de pointer un
// pipeline vers un autre dépôt que celui prévu.
//
// Ce qui n'est **pas** déclaré passe en revanche tel quel, et c'est ce qui rend
// possible une exécution personnalisée : le formulaire accepte des paramètres
// supplémentaires, que le catalogue ignore et que le pipeline peut connaître.
// La règle ci-dessus n'en est pas affaiblie — un paramètre déclaré garde sa
// source, et une valeur fournie sous le nom d'un paramètre `config` est écartée
// par la boucle qui suit, non reprise par celle d'après.
//
// Un paramètre supplémentaire que le Pipeline ne déclare pas fait échouer la
// création côté Tekton, avec un message qui le nomme. C'est le comportement
// voulu : mieux vaut un refus explicite qu'un paramètre silencieusement perdu.
func (s *Scheduler) resolveParams(
	pipeline catalog.Pipeline, provided map[string]string,
) (map[string]string, error) {
	params := make(map[string]string, len(pipeline.Params))

	for name, declared := range pipeline.Params {
		switch declared.Source {
		case catalog.SourceLiteral:
			params[name] = declared.Value

		case catalog.SourceConfig:
			value, present := s.config[declared.Key]
			if !present {
				return nil, fmt.Errorf("paramètre %q: clé de configuration %q absente", name, declared.Key)
			}
			params[name] = value

		case catalog.SourceInput:
			value, present := provided[name]
			if !present || value == "" {
				if declared.Required {
					return nil, fmt.Errorf("paramètre %q requis et non fourni", name)
				}
				continue
			}
			params[name] = value
		}
	}

	for name, value := range provided {
		if _, declared := pipeline.Params[name]; declared {
			// Déjà traité par sa source. Le reprendre ici écraserait une valeur
			// de configuration par une valeur d'utilisateur, ce que toute la
			// séparation des sources existe pour empêcher.
			continue
		}
		if value == "" {
			continue
		}
		params[name] = value
	}

	return params, nil
}

// RunName compose le nom du PipelineRun d'un job.
//
// Il porte l'identifiant du job : le lien est lisible dans `kubectl` sans
// consulter la base, et deux essais du même job donnent le même nom. C'est ce qui
// rend la reprise sûre — une seconde création se heurte à un conflit, que
// l'ordonnanceur lit comme « c'était déjà fait » plutôt que de créer un doublon.
//
// Il porte **aussi** l'identité de la base, et c'est ce qui manquait. Les
// identifiants de jobs repartent à 1 sur une base neuve, alors que les
// PipelineRun survivent dans le cluster : chaque nouveau job s'accrochait à un
// run existant, le conflit était lu comme un succès, et rien ne partait. On
// regardait l'échec d'une exécution vieille de plusieurs heures en croyant
// consulter la sienne.
//
// L'identité vide est tolérée pour que les tests n'aient pas à la fournir ; en
// production elle est lue au démarrage et ne l'est jamais.
func RunName(instance, kind string, id int64) string {
	if instance == "" {
		return fmt.Sprintf("mse-%s-%d", kind, id)
	}
	return fmt.Sprintf("mse-%s-%s-%d", instance, kind, id)
}

// retry remet un job en file, ou le conclut en échec quand il a trop insisté.
//
// La distinction est ce qui empêche un job insatisfiable d'immobiliser la file.
// Une reprise sert aux échecs transitoires ; un paramètre manquant, lui, ne se
// matérialisera jamais — les paramètres sont figés à la mise en file, et le
// catalogue évolue. Sans plafond, l'ordonnanceur reprend indéfiniment le même
// job, toujours le plus ancien, et ne considère jamais les suivants.
func (s *Scheduler) retry(ctx context.Context, jobID int64, reason string) {
	again, err := s.queue.Requeue(ctx, jobID, reason)
	if err != nil {
		s.logger.Error("remise en file", "job", jobID, "error", err)
		return
	}
	if again {
		return
	}

	// Le motif de la dernière tentative est conservé : sans lui, l'utilisateur
	// lirait « trop de tentatives » sans savoir de quoi.
	final := fmt.Sprintf("abandonné après %d tentatives: %s", store.MaxJobAttempts, reason)
	s.logger.Error("job abandonné", "job", jobID, "tentatives", store.MaxJobAttempts, "raison", reason)
	if err := s.queue.FinishJob(ctx, jobID, store.JobFailed, final); err != nil {
		s.logger.Error("conclusion du job abandonné", "job", jobID, "error", err)
	}
}
