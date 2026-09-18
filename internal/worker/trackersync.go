package worker

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/provider"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/release"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Synchronisation Redmine.
 *
 * Interrogation périodique incrémentale. C'est la voie définitive et non un repli :
 * aucun plugin webhook n'est déployable sur l'instance (B6), donc rien ne peut nous
 * prévenir d'un changement.
 *
 * # Ce qui reste ici, et ce qui est parti
 *
 * La **projection** reste : lire les versions, les déposer, adopter les
 * provisoires, projeter les demandes sur leurs seaux. C'est le travail propre de
 * ce paquet, et il n'a pas bougé.
 *
 * La **boucle** est partie. Elle vit dans `probes.go`, avec celle de la forge :
 * résoudre les réglages du plugin, desceller la clé, lire et avancer le curseur,
 * isoler l'échec d'un projet. Les deux sources faisaient exactement cela, chacune
 * à sa façon — et la seconde version était la copie de la première, écrite six
 * mois plus tard sans le savoir.
 *
 * Ce que la migration a rapporté est consigné en doc 14 §14.11 : deux manques du
 * contrat que la seule sonde Gitea ne pouvait pas révéler.
 */

// redmineSource nomme la source dans `sync_state`.
//
// **Inchangé, et c'est délibéré.** Les curseurs des projets sont écrits sous ce
// nom depuis le lot C. Le renommer en aurait fait repartir chacun de zéro, donc
// relire l'intégralité de l'historique d'une instance qui n'est pas la nôtre —
// pour une amélioration cosmétique.
const redmineSource = "redmine"

// syncInterval borne la fréquence d'interrogation.
//
// Cinq minutes : les versions et les demandes bougent au rythme d'une saisie
// humaine, et interroger plus souvent chargerait une instance qui n'est pas la nôtre
// pour des données identiques. Plus rarement rendrait l'écran des versions
// visiblement en retard sur Redmine, où l'utilisateur vient de saisir.
const syncInterval = 5 * time.Minute

// Les faits que le suivi constate au passage.
//
// Deux, et pas davantage. Ce sont ceux que doc 14 §14.4 avait nommés, et ils ont
// en commun d'être des **événements** — quelque chose est arrivé une fois. Une
// demande qui change de statut n'en est pas un : elle en change dix fois, et un
// fait par changement ferait du fil des faits un journal de saisie.
//
// # Un seul des deux porte le préfixe du plugin
//
// La version est devenue un fait du **noyau** — `version.created` —, parce que
// tous les suivis déposent leurs versions dans la même table et qu'une réaction
// déclarée dessus n'a pas à être réécrite quand le client change d'outil
// (doc 15 §15.3). La demande n'a pas d'équivalent au noyau : elle reste un fait
// de Redmine, et son nom le dit.
const redmineIssueClosed = "redmine.issue.closed"

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

// TrackerStore est ce que la projection attend de la base.
//
// **Quatre méthodes, contre neuf avant la migration.** Les cinq qui manquent —
// les projets suivis, les réglages du plugin, le curseur, ses deux issues — sont
// passées à la boucle des sondes, qui les tient pour toutes les sources. C'est la
// mesure la plus nette de ce que le contrat a absorbé : ce qui reste ici est le
// travail propre au suivi, et rien d'autre.
type TrackerStore interface {
	UpsertRedmineVersion(ctx context.Context, version store.TrackedVersion) (int64, error)
	UpsertRedmineIssue(ctx context.Context, issue store.TrackedIssue) error
	AdoptPendingVersions(ctx context.Context) ([]store.Adoption, error)
	StatusMapping(ctx context.Context, projectID int64) (map[string]string, error)
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
	logger  *slog.Logger
	now     func() time.Time
}

func NewTrackerSync(
	db TrackerStore, connect TrackerConnector, logger *slog.Logger,
) *TrackerSync {
	if logger == nil {
		logger = slog.Default()
	}
	return &TrackerSync{
		store: db, connect: connect,
		logger: logger, now: time.Now,
	}
}

// Observe exécute une passe complète pour un projet, et rend ce qu'elle constate.
//
// Appelée par la sonde, qui lui apporte le client déjà connecté et le curseur.
// Elle ne décide ni de son moment, ni de ce qu'il advient de ses faits.
//
// L'ordre est imposé : les versions d'abord, les demandes ensuite. Une demande se
// rattache à sa version par son identifiant externe, et lire les demandes en
// premier laisserait sans rattachement toutes celles visant une version
// nouvellement créée.
func (s *TrackerSync) Observe(
	ctx context.Context, projectID int64, trackerProject string,
	client TrackerReader, cursor string,
) ([]event.Event, string, error) {
	// L'instant est capturé **avant** la lecture, et c'est ce qui rend le curseur
	// sûr : le prendre après ferait du temps de lecture un angle mort, les demandes
	// modifiées pendant celle-ci n'étant ni dans cette passe ni dans la suivante.
	startedAt := s.now().UTC()

	// Les versions sont relues en entier à chaque passe. L'API ne les filtre pas sur
	// `updated_on`, et leur nombre est de l'ordre de la vingtaine : une lecture
	// complète coûte une requête.
	//
	// C'est aussi ce qui fait de cette sonde un **instantané** plutôt qu'un flux,
	// et ce qui a révélé le défaut d'amorçage corrigé dans `probes.go`.
	versions, err := client.ListVersions(ctx, trackerProject)
	if err != nil {
		return nil, "", err
	}

	facts := make([]event.Event, 0, len(versions))
	for _, version := range versions {
		versionID, err := s.store.UpsertRedmineVersion(ctx, store.TrackedVersion{
			ProjectID: projectID,
			// L'outil et l'identifiant qu'il emploie, plutôt qu'un entier dont le
			// nom disait déjà de quel produit il venait.
			Tool:        trackerTool,
			ExternalID:  version.ID,
			Name:        version.Name,
			Normalized:  release.NormalizeVersionName(version.Name),
			Description: version.Description,
			Status:      version.Status,
			DueDate:     version.DueDate,
			UpdatedOn:   version.UpdatedOn,
		})
		if err != nil {
			return nil, "", err
		}
		facts = append(facts, versionCreatedFact(versionID, version, startedAt))
	}

	// L'adoption vient après le dépôt de **toutes** les versions : c'est ce qui la
	// rend indépendante de l'ordre de lecture quand deux noms se réduisent au même
	// numéro.
	adoptions, err := s.store.AdoptPendingVersions(ctx)
	if err != nil {
		return nil, "", err
	}
	s.reportAdoptions(adoptions)

	mapping, err := s.store.StatusMapping(ctx, projectID)
	if err != nil {
		return nil, "", err
	}

	since := cursorSince(cursor)
	issues, err := client.ListIssues(ctx, provider.IssueQuery{Project: trackerProject, Since: since})
	if err != nil {
		return nil, "", err
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
			ProjectID:  projectID,
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
			return nil, "", err
		}
		if issue.StatusClosed {
			facts = append(facts, issueClosedFact(trackerProject, issue, startedAt))
		}
	}

	for status, count := range unmapped {
		s.logger.Warn("statut Redmine sans projection déclarée",
			"projet", trackerProject, "status", status,
			"issues", count, "bucket_par_defaut", "todo")
	}

	s.logger.Info("synchronisation du suivi",
		"projet", trackerProject,
		"versions", len(versions), "demandes", len(issues),
		"incrementale", !since.IsZero(), "statuts_non_projetes", len(unmapped))

	return facts, startedAt.Format(time.RFC3339), nil
}

// versionCreatedFact constate qu'une version existe dans le suivi.
//
// # Pourquoi « created » sur une lecture qui rend tout
//
// Chaque passe relit l'intégralité des versions, et produit donc un fait par
// version à chaque tour. Ce n'est pas un gaspillage : la **clé de déduplication**
// porte la version et rien d'autre, si bien qu'une version déjà vue n'est écrite
// qu'une fois. La création est donc déduite de l'unicité, sans que le magasin ait
// à dire si son écriture a inséré ou mis à jour.
//
// C'est cette forme — un instantané rendu en entier, dédupliqué par clé — que le
// contrat de sonde ne connaissait pas avant cette migration.
//
// # La clé porte la version locale, et c'est ce qui a changé
//
// Elle portait le projet du suivi et l'identifiant Redmine. Depuis que le fait
// est du noyau, elle porte la **ligne déposée** : deux suivis branchés sur le
// même projet ne constatent plus deux fois la même version, et une instance qui
// changerait d'outil ne reconstaterait pas tout son historique.
func versionCreatedFact(
	versionID int64, version provider.Version, fallback time.Time,
) event.Event {
	return event.VersionCreatedAt(
		// La date de mise à jour dans le suivi, faute de mieux : Redmine ne rend
		// pas la date de création d'une version. Elle vaut celle du fait le jour
		// où il est neuf, et c'est le seul jour où il compte.
		orNow(version.UpdatedOn, fallback),
		versionID, version.Name, version.Status,
		trackerTool, version.ID, version.DueDate)
}

// issueClosedFact constate qu'une demande est close.
//
// La clé ne porte pas le statut : une demande rouverte puis reclose ne produit pas
// un second fait. C'est délibéré — le fait dit « cette demande a été close », pas
// « elle vient de l'être », et une équipe qui rouvre pour corriger un libellé
// n'a pas à renvoyer une notification de livraison.
func issueClosedFact(
	trackerProject string, issue provider.Issue, fallback time.Time,
) event.Event {
	payload := map[string]string{
		"issue_id": issue.ID,
		"subject":  issue.Subject,
		"tracker":  issue.Tracker,
		"status":   issue.Status,
	}
	if issue.VersionID != nil {
		payload["version_id"] = *issue.VersionID
	}

	return event.Event{
		Type:       redmineIssueClosed,
		Subject:    "#" + issue.ID,
		Payload:    payload,
		OccurredAt: orNow(issue.UpdatedOn, fallback),
		DedupKey:   redmineIssueClosed + ":" + trackerProject + ":" + issue.ID,
	}
}

// orNow rend la date du suivi, ou celle de la passe à défaut.
//
// Une date absente ne doit pas produire un fait daté de l'an zéro : le fil des
// faits est ordonné par elle, et un tel fait s'y enterrerait sous tout le reste.
// L'instant de la passe est une approximation honnête — c'est le moment où on
// l'a constaté, faute de savoir quand il a eu lieu.
func orNow(moment *time.Time, fallback time.Time) time.Time {
	if moment == nil || moment.IsZero() {
		return fallback
	}
	return *moment
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
