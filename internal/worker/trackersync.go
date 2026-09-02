package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/provider"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/release"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Synchronisation Redmine.
 *
 * Interrogation périodique incrémentale. C'est la voie définitive et non un repli :
 * aucun plugin webhook n'est déployable sur l'instance (B6), donc rien ne peut nous
 * prévenir d'un changement.
 */

// redmineSource nomme la source dans `sync_state`.
const redmineSource = "redmine"

// syncInterval borne la fréquence d'interrogation.
//
// Cinq minutes : les versions et les demandes bougent au rythme d'une saisie
// humaine, et interroger plus souvent chargerait une instance qui n'est pas la nôtre
// pour des données identiques. Plus rarement rendrait l'écran des versions
// visiblement en retard sur Redmine, où l'utilisateur vient de saisir.
const syncInterval = 5 * time.Minute

// cursorOverlap est la marge retirée au curseur à chaque lecture.
//
// Redmine filtre sur `updated_on` avec la précision de la seconde, et son horloge
// n'est pas la nôtre. Repartir exactement du dernier instant lu ferait manquer toute
// demande modifiée dans la même seconde que la fin de la lecture précédente. Une
// minute de recouvrement fait relire quelques demandes — l'écriture étant idempotente
// — plutôt que d'en perdre une définitivement.
const cursorOverlap = time.Minute

// TrackerReader est ce que la synchronisation attend du client.
type TrackerReader interface {
	ListVersions(ctx context.Context, project string) ([]provider.Version, error)
	ListIssues(ctx context.Context, query provider.IssueQuery) ([]provider.Issue, error)
}

// TrackerConnection est ce qu'il faut savoir pour interroger un suivi.
//
// Résolue par projet : le suivi est un plugin, et ses réglages se superposent —
// ce que l'instance déclare, ce que le projet corrige. C'est ce qui permet à deux
// clients d'avoir chacun leur serveur sans que le premier ait rien à ressaisir.
type TrackerConnection struct {
	URL string
	// APIKey est **en clair** : le déscellement a eu lieu juste avant, et cette
	// valeur ne quitte pas le worker. Elle ne doit apparaître ni dans un log, ni
	// dans un message d'erreur, ni dans une URL.
	APIKey string
	// Project est l'identifiant du projet **dans le suivi**, tel qu'il apparaît
	// dans son URL.
	Project string
}

// TrackerConnector construit un client pour une connexion donnée.
//
// Une fonction plutôt qu'un client déjà bâti : la connexion n'est plus la même
// pour tous les projets, et un client unique construit au démarrage ne pourrait
// en servir qu'un seul.
type TrackerConnector func(TrackerConnection) (TrackerReader, error)

// TrackerStore est ce qu'elle attend de la base.
type TrackerStore interface {
	// TrackedProjects nomme les projets qui ont un outil de suivi. Les autres
	// n'ont rien à lire.
	TrackedProjects(ctx context.Context) ([]store.TrackedProject, error)
	// ResolvedPlugin rend les réglages effectifs d'un plugin pour un projet,
	// **champs sensibles encore scellés**.
	ResolvedPlugin(ctx context.Context, projectID int64, pluginID string) (store.PluginSettings, error)
	UpsertRedmineVersion(ctx context.Context, version store.TrackedVersion) (int64, error)
	UpsertRedmineIssue(ctx context.Context, issue store.TrackedIssue) error
	AdoptPendingVersions(ctx context.Context) ([]store.Adoption, error)
	StatusMapping(ctx context.Context, projectID int64) (map[string]string, error)
	GetSyncState(ctx context.Context, source string, projectID int64) (store.SyncState, error)
	RecordSyncSuccess(ctx context.Context, source string, projectID int64, cursor string) error
	RecordSyncFailure(ctx context.Context, source string, projectID int64, message string) error
}

// SecretOpener déscelle les champs sensibles des réglages.
//
// Interface étroite plutôt que le chiffreur entier : la synchronisation n'a
// besoin que d'ouvrir, jamais de sceller, et lui donner le scellement
// l'inviterait à écrire des secrets qu'elle n'a aucune raison d'écrire.
type SecretOpener interface {
	Open(sealed string) (string, error)
	Configured() bool
}

// TrackerSync projette les versions et les demandes en base.
type TrackerSync struct {
	store   TrackerStore
	connect TrackerConnector
	secrets SecretOpener
	// names calcule la forme comparable écrite en base. Doit être **la même** que
	// celle du store : deux normaliseurs divergents rendraient une release
	// incapable de trouver sa version, sans le moindre message.
	names  release.Normalizer
	logger *slog.Logger
	now    func() time.Time
}

func NewTrackerSync(
	db TrackerStore, connect TrackerConnector, opener SecretOpener, logger *slog.Logger,
) *TrackerSync {
	return &TrackerSync{
		store: db, connect: connect, secrets: opener,
		names: release.DefaultNormalizer, logger: logger, now: time.Now,
	}
}

// WithNormalizer déclare les préfixes retirés pour comparer deux noms de version.
func (s *TrackerSync) WithNormalizer(names release.Normalizer) *TrackerSync {
	s.names = names
	return s
}

// Run synchronise jusqu'à annulation.
//
// Un échec ne fait pas tomber la boucle : Redmine est un service tiers, et sa panne
// ne doit pas emporter l'ordonnanceur ni l'observation, qui n'ont rien à voir. Il est
// consigné dans `sync_state`, où l'interface le lit pour dire « données vieilles de
// tant, dernière tentative en échec ».
func (s *TrackerSync) Run(ctx context.Context) error {
	// Un premier passage immédiat : au démarrage, l'écran des versions serait vide
	// jusqu'au premier battement.
	s.once(ctx)

	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			s.once(ctx)
		}
	}
}

func (s *TrackerSync) once(ctx context.Context) {
	if err := s.Sync(ctx); err != nil {
		s.logger.Error("synchronisation du suivi", "error", err)
	}
}

// Sync synchronise chaque projet suivi, indépendamment des autres.
//
// L'échec d'un projet **n'arrête pas** les suivants : deux projets peuvent viser
// deux instances Redmine, et la panne de l'une ne dit rien de l'autre. Chacun
// consigne son propre état, ce que l'écran du projet lit.
//
// Les projets **suivis**, et eux seuls : un projet sans outil de suivi n'a rien à
// lire, et l'interroger demanderait un nom de projet qu'il ne déclare pas.
func (s *TrackerSync) Sync(ctx context.Context) error {
	tracked, err := s.store.TrackedProjects(ctx)
	if err != nil {
		return err
	}

	var problems []error
	for _, project := range tracked {
		if err := s.syncProject(ctx, project); err != nil {
			problems = append(problems, fmt.Errorf("projet %q: %w", project.Slug, err))
			// L'enregistrement de l'échec ne doit pas masquer l'échec lui-même.
			if recordErr := s.store.RecordSyncFailure(
				ctx, redmineSource, project.ID, err.Error()); recordErr != nil {
				s.logger.Error("enregistrement de l'échec de synchronisation",
					"projet", project.Slug, "error", recordErr)
			}
		}
	}
	return errors.Join(problems...)
}

// connectionFor résout la connexion d'un projet à partir des réglages du plugin.
//
// C'est ici que la clé d'API est **descellée**, et le seul endroit : elle
// n'existe en clair que le temps de construire le client, dans un processus qui
// n'expose aucun port.
func (s *TrackerSync) connectionFor(ctx context.Context, project store.TrackedProject) (TrackerConnection, error) {
	settings, err := s.store.ResolvedPlugin(ctx, project.ID, trackerTool)
	if err != nil {
		return TrackerConnection{}, err
	}

	apiKey, err := s.secrets.Open(settings.Config["api_key"])
	if err != nil {
		// Le message ne cite pas la valeur, et pour cause : c'est justement celle
		// qu'on n'a pas su ouvrir.
		return TrackerConnection{}, fmt.Errorf("clé d'API du suivi illisible: %w", err)
	}

	// L'identifiant du projet dans le suivi vient du réglage, et retombe sur la
	// colonne `tracker_project` : elle a précédé les plugins, elle est juste, et
	// un déploiement dont l'amorçage n'aurait pas tourné ne doit pas cesser de se
	// synchroniser pour autant.
	trackerProject := settings.Config["project"]
	if strings.TrimSpace(trackerProject) == "" {
		trackerProject = project.TrackerProject
	}

	return TrackerConnection{
		URL: settings.Config["url"], APIKey: apiKey, Project: trackerProject,
	}, nil
}

// syncProject exécute une passe complète pour un projet.
//
// L'ordre est imposé : les versions d'abord, les demandes ensuite. Une demande se
// rattache à sa version par son identifiant externe, et lire les demandes en premier laisserait
// sans rattachement toutes celles visant une version nouvellement créée.
func (s *TrackerSync) syncProject(ctx context.Context, project store.TrackedProject) error {
	connection, err := s.connectionFor(ctx, project)
	if err != nil {
		return err
	}
	client, err := s.connect(connection)
	if err != nil {
		return err
	}

	state, err := s.store.GetSyncState(ctx, redmineSource, project.ID)
	if err != nil {
		return err
	}

	// L'instant est capturé **avant** la lecture, et c'est ce qui rend le curseur
	// sûr : le prendre après ferait du temps de lecture un angle mort, les demandes
	// modifiées pendant celle-ci n'étant ni dans cette passe ni dans la suivante.
	startedAt := s.now().UTC()

	// Les versions sont relues en entier à chaque passe. L'API ne les filtre pas sur
	// `updated_on`, et leur nombre est de l'ordre de la vingtaine : une lecture
	// complète coûte une requête.
	versions, err := client.ListVersions(ctx, connection.Project)
	if err != nil {
		return err
	}
	for _, version := range versions {
		if _, err := s.store.UpsertRedmineVersion(ctx, store.TrackedVersion{
			ProjectID: project.ID,
			// L'outil et l'identifiant qu'il emploie, plutôt qu'un entier dont le
			// nom disait déjà de quel produit il venait.
			Tool:        trackerTool,
			ExternalID:  version.ID,
			Name:        version.Name,
			Normalized:  s.names.Normalize(version.Name),
			Description: version.Description,
			Status:      version.Status,
			DueDate:     version.DueDate,
			UpdatedOn:   version.UpdatedOn,
		}); err != nil {
			return err
		}
	}

	// L'adoption vient après le dépôt de **toutes** les versions : c'est ce qui la
	// rend indépendante de l'ordre de lecture quand deux noms se réduisent au même
	// numéro.
	adoptions, err := s.store.AdoptPendingVersions(ctx)
	if err != nil {
		return err
	}
	s.reportAdoptions(adoptions)

	mapping, err := s.store.StatusMapping(ctx, project.ID)
	if err != nil {
		return err
	}

	since := cursorSince(state.Cursor)
	issues, err := client.ListIssues(ctx, provider.IssueQuery{Project: connection.Project, Since: since})
	if err != nil {
		return err
	}

	unmapped := map[string]int{}
	for _, issue := range issues {
		bucket, known := mapping[issue.Status]
		if !known {
			// Le seau par défaut, et un compte pour le journal. Le statut apparaîtra
			// aussi dans l'écran d'administration par `UnmappedStatuses` : les statuts
			// de l'instance changent sans prévenir, et un ajout non projeté ferait
			// dériver le taux de complétion sans que rien ne le signale.
			bucket = "todo"
			unmapped[issue.Status]++
		}

		if err := s.store.UpsertRedmineIssue(ctx, store.TrackedIssue{
			ProjectID:  project.ID,
			Tool:       trackerTool,
			ExternalID: issue.ID,
			// La version visée, désignée par son identifiant **dans l'outil** : le
			// dashboard ne connaît pas encore sa clé locale au moment où il
			// projette la demande.
			VersionExternalID: issue.VersionID,
			Tracker:           issue.Tracker,
			Subject:           issue.Subject,
			StatusName:        issue.Status,
			StatusBucket:      bucket,
			Priority:          issue.Priority,
			Assignee:          issue.Assignee,
			DoneRatio:         issue.DoneRatio,
			IsClosed:          issue.StatusClosed,
			UpdatedOn:         issue.UpdatedOn,
		}); err != nil {
			return err
		}
	}

	for status, count := range unmapped {
		s.logger.Warn("statut Redmine sans projection déclarée",
			"projet", project.TrackerProject, "status", status,
			"issues", count, "bucket_par_defaut", "todo")
	}

	s.logger.Info("synchronisation du suivi",
		"projet", project.TrackerProject,
		"versions", len(versions), "demandes", len(issues),
		"incrementale", !since.IsZero(), "statuts_non_projetes", len(unmapped))

	return s.store.RecordSyncSuccess(ctx, redmineSource, project.ID,
		startedAt.Format(time.RFC3339))
}

// reportAdoptions journalise le rapprochement des versions provisoires.
//
// Une ambiguïté est un avertissement et non une erreur : elle vient du nommage côté
// client, pas d'une défaillance du dashboard, et la synchronisation doit aboutir.
func (s *TrackerSync) reportAdoptions(adoptions []store.Adoption) {
	for _, adoption := range adoptions {
		if adoption.Ambiguous {
			s.logger.Warn("version provisoire revendiquée par plusieurs versions Redmine",
				"provisoire", adoption.PendingName,
				"candidates", adoption.Candidates,
				"consequence", "rattachement laissé en attente, à trancher à la main")
			continue
		}
		s.logger.Info("version provisoire adoptée",
			"provisoire", adoption.PendingName,
			"version", adoption.AdoptedInto,
			"releases", adoption.Releases)
	}
}

// cursorSince rend l'instant de reprise, marge de recouvrement comprise.
//
// Un curseur illisible est traité comme une absence de curseur : relire tout est
// coûteux mais correct, là où repartir d'un instant erroné perdrait des demandes en
// silence.
func cursorSince(cursor string) time.Time {
	if cursor == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, cursor)
	if err != nil {
		return time.Time{}
	}
	return parsed.Add(-cursorOverlap)
}

// ForceResync efface le curseur d'un projet pour que la passe suivante relise
// tout.
//
// Exposé à l'administration : après une correction du mapping des statuts ou un doute
// sur la complétude des données, relire l'ensemble est la seule façon de repartir
// d'un état connu.
//
// Borné à un projet : relire l'ensemble sollicite un service tiers, et rien ne
// justifie de faire porter à un client le doute qu'on a sur un autre.
func ForceResync(ctx context.Context, projectID int64, resetter interface {
	ResetSyncCursor(ctx context.Context, source string, projectID int64) error
}) error {
	if err := resetter.ResetSyncCursor(ctx, redmineSource, projectID); err != nil {
		return fmt.Errorf("réinitialisation du curseur Redmine: %w", err)
	}
	return nil
}

// Même garde que côté HTTP : ce que le worker demande à ses sources doit rester
// un sous-ensemble de ce que la frontière annonce, sinon le découplage futur
// perdra en silence les appelants qui ont débordé.
var (
	_ TrackerReader = provider.Tracker(nil)
	_ PullReader    = provider.Forge(nil)
)

// trackerTool nomme l'outil que ce synchroniseur interroge.
//
// Constante ici et non paramètre : ce paquet **est** l'implémentation Redmine.
// Le jour où un second existera, c'est un autre type qui portera son nom, et le
// choix se fera à la construction plutôt que par un réglage de celui-ci.
const trackerTool = "redmine"

// externalRef traduit un identifiant numérique en référence textuelle.
//
// Nul reste nul : une demande sans version n'en vise aucune, et `"0"` désignerait
// une version qui n'existe pas.
func externalRef(id *int64) *string {
	if id == nil {
		return nil
	}
	reference := strconv.FormatInt(*id, 10)
	return &reference
}
