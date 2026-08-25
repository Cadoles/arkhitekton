package worker

import (
	"context"
	"fmt"
	"log/slog"
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

// RedmineReader est ce que la synchronisation attend du client.
type RedmineReader interface {
	ListVersions(ctx context.Context, project string) ([]provider.Version, error)
	ListIssues(ctx context.Context, query provider.IssueQuery) ([]provider.Issue, error)
}

// RedmineStore est ce qu'elle attend de la base.
type RedmineStore interface {
	UpsertRedmineVersion(ctx context.Context, version store.RedmineVersion) (int64, error)
	UpsertRedmineIssue(ctx context.Context, issue store.RedmineIssue) error
	AdoptPendingVersions(ctx context.Context) ([]store.Adoption, error)
	StatusMapping(ctx context.Context) (map[string]string, error)
	GetSyncState(ctx context.Context, source string) (store.SyncState, error)
	RecordSyncSuccess(ctx context.Context, source, cursor string) error
	RecordSyncFailure(ctx context.Context, source, message string) error
}

// RedmineSync projette les versions et les demandes en base.
type RedmineSync struct {
	store   RedmineStore
	client  RedmineReader
	project string
	// names calcule la forme comparable écrite en base. Doit être **la même** que
	// celle du store : deux normaliseurs divergents rendraient une release
	// incapable de trouver sa version, sans le moindre message.
	names  release.Normalizer
	logger *slog.Logger
	now    func() time.Time
}

func NewRedmineSync(db RedmineStore, client RedmineReader, project string, logger *slog.Logger) *RedmineSync {
	return &RedmineSync{
		store: db, client: client, project: project,
		names: release.DefaultNormalizer, logger: logger, now: time.Now,
	}
}

// WithNormalizer déclare les préfixes retirés pour comparer deux noms de version.
func (s *RedmineSync) WithNormalizer(names release.Normalizer) *RedmineSync {
	s.names = names
	return s
}

// Run synchronise jusqu'à annulation.
//
// Un échec ne fait pas tomber la boucle : Redmine est un service tiers, et sa panne
// ne doit pas emporter l'ordonnanceur ni l'observation, qui n'ont rien à voir. Il est
// consigné dans `sync_state`, où l'interface le lit pour dire « données vieilles de
// tant, dernière tentative en échec ».
func (s *RedmineSync) Run(ctx context.Context) error {
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

func (s *RedmineSync) once(ctx context.Context) {
	if err := s.Sync(ctx); err != nil {
		s.logger.Error("synchronisation Redmine", "error", err)
		// L'enregistrement de l'échec ne doit pas masquer l'échec lui-même.
		if recordErr := s.store.RecordSyncFailure(ctx, redmineSource, err.Error()); recordErr != nil {
			s.logger.Error("enregistrement de l'échec de synchronisation", "error", recordErr)
		}
	}
}

// Sync exécute une passe complète.
//
// L'ordre est imposé : les versions d'abord, les demandes ensuite. Une demande se
// rattache à sa version par `redmine_id`, et lire les demandes en premier laisserait
// sans rattachement toutes celles visant une version nouvellement créée.
func (s *RedmineSync) Sync(ctx context.Context) error {
	state, err := s.store.GetSyncState(ctx, redmineSource)
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
	versions, err := s.client.ListVersions(ctx, s.project)
	if err != nil {
		return err
	}
	for _, version := range versions {
		if _, err := s.store.UpsertRedmineVersion(ctx, store.RedmineVersion{
			RedmineID:   version.ID,
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

	mapping, err := s.store.StatusMapping(ctx)
	if err != nil {
		return err
	}

	since := cursorSince(state.Cursor)
	issues, err := s.client.ListIssues(ctx, provider.IssueQuery{Project: s.project, Since: since})
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

		if err := s.store.UpsertRedmineIssue(ctx, store.RedmineIssue{
			RedmineID:        issue.ID,
			VersionRedmineID: issue.VersionID,
			Tracker:          issue.Tracker,
			Subject:          issue.Subject,
			StatusName:       issue.Status,
			StatusBucket:     bucket,
			Priority:         issue.Priority,
			Assignee:         issue.Assignee,
			DoneRatio:        issue.DoneRatio,
			IsClosed:         issue.StatusClosed,
			UpdatedOn:        issue.UpdatedOn,
		}); err != nil {
			return err
		}
	}

	for status, count := range unmapped {
		s.logger.Warn("statut Redmine sans projection déclarée",
			"status", status, "issues", count, "bucket_par_defaut", "todo")
	}

	s.logger.Info("synchronisation Redmine",
		"versions", len(versions), "demandes", len(issues),
		"incrementale", !since.IsZero(), "statuts_non_projetes", len(unmapped))

	return s.store.RecordSyncSuccess(ctx, redmineSource, startedAt.Format(time.RFC3339))
}

// reportAdoptions journalise le rapprochement des versions provisoires.
//
// Une ambiguïté est un avertissement et non une erreur : elle vient du nommage côté
// client, pas d'une défaillance du dashboard, et la synchronisation doit aboutir.
func (s *RedmineSync) reportAdoptions(adoptions []store.Adoption) {
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

// ForceResync efface le curseur pour que la passe suivante relise tout.
//
// Exposé à l'administration : après une correction du mapping des statuts ou un doute
// sur la complétude des données, relire l'ensemble est la seule façon de repartir
// d'un état connu.
func ForceResync(ctx context.Context, resetter interface {
	ResetSyncCursor(ctx context.Context, source string) error
}) error {
	if err := resetter.ResetSyncCursor(ctx, redmineSource); err != nil {
		return fmt.Errorf("réinitialisation du curseur Redmine: %w", err)
	}
	return nil
}

// Même garde que côté HTTP : ce que le worker demande à ses sources doit rester
// un sous-ensemble de ce que la frontière annonce, sinon le découplage futur
// perdra en silence les appelants qui ont débordé.
var (
	_ RedmineReader = provider.Tracker(nil)
	_ PullReader    = provider.Forge(nil)
)
