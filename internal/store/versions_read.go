package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrVersionNotFoundByID signale une version absente.
var ErrVersionNotFoundByID = errors.New("version introuvable")

// VersionSummary est une version et son avancement.
//
// Deux mesures de complétion, parce qu'elles ne disent pas la même chose (doc 04
// §4.5) : par nombre de demandes, lisible mais une grosse demande vaut une petite ;
// pondérée par `done_ratio`, plus fine mais tributaire de la rigueur de saisie.
type VersionSummary struct {
	ID int64
	// ProjectID rattache la version à son dépôt. Nécessaire pour savoir quel
	// schéma de version et quel cycle de release s'y appliquent.
	ProjectID int64
	// ExternalID est l'identifiant de la version dans le suivi, vide quand elle
	// n'en vient pas.
	ExternalID string
	Name       string
	Status     string
	DueDate    *time.Time
	SyncedAt   time.Time

	// Pending marque une version provisoire, créée par une release avant que la
	// synchronisation ne connaisse la version réelle.
	Pending bool

	// Counted est le dénominateur : les demandes hors `rejected`. Les rejetées ne
	// pèsent ni au numérateur ni au dénominateur — les compter en bas ferait baisser
	// la complétion d'une version parce qu'on y a renoncé à une demande.
	Counted int
	Done    int
	// WeightedDone est la somme des `done_ratio` des demandes comptées, en points de
	// pourcentage. Divisée par `Counted` elle donne la seconde mesure.
	WeightedDone int

	// OpenAnomalies est compté à part et **n'entre pas** dans la complétion : une
	// version peut être fonctionnellement complète et porter des anomalies ouvertes,
	// ce sont deux signaux distincts qui doivent le rester.
	OpenAnomalies int

	Releases int
}

// Buckets est la projection des statuts Redmine, dans l'ordre d'avancement.
//
// Un seul endroit, et ordonné : les colonnes de l'écran, la liste du sélecteur
// d'administration et la contrainte de la base doivent dire la même chose. Trois
// énumérations séparées divergeraient au premier ajout — c'est précisément ce qui
// vient d'arriver avec `client-test`.
//
// `rejected` est en dehors de l'avancement : il n'est ni avant ni après, il est hors
// périmètre (doc 04 §4.5).
var Buckets = []string{BucketTodo, BucketDoing, BucketClientTest, BucketDone}

const (
	BucketTodo  = "todo"
	BucketDoing = "doing"
	// BucketClientTest est l'attente de validation par le client : le développement
	// est fini, la recette non.
	//
	// **Il ne compte pas comme terminé.** Il entre au dénominateur de la complétion,
	// jamais au numérateur — confondre « fini de développer » et « accepté » est
	// exactement ce qu'un taux de complétion doit éviter.
	BucketClientTest = "client-test"
	BucketDone       = "done"
	// BucketRejected sort du calcul : ni au numérateur ni au dénominateur. Le compter
	// en bas ferait baisser la complétion d'une version parce qu'on y a renoncé à une
	// demande.
	BucketRejected = "rejected"
)

// OpenBuckets sont les seaux d'une demande encore à traiter.
//
// C'est **cette** liste qui décide si une anomalie est ouverte, et non le drapeau
// `is_closed` de Redmine. Les deux divergent sur l'instance réelle : `Testé` y est
// déclaré non clos alors qu'il est projeté en `done`, et `Ré-ouvert` l'inverse. Compter
// les anomalies sur `is_closed` affichait donc cinq anomalies terminées comme ouvertes.
//
// Employer deux notions de « clos » dans le même écran est la faute de fond ; la
// projection est celle que l'utilisateur règle et voit, donc la seule autorité.
//
// `client-test` en est absent : une anomalie en attente de validation client n'est plus
// à traiter par l'équipe.
var OpenBuckets = []string{BucketTodo, BucketDoing}

// BucketIsOpen dit si un seau désigne une demande encore à traiter.
func BucketIsOpen(bucket string) bool {
	for _, open := range OpenBuckets {
		if open == bucket {
			return true
		}
	}
	return false
}

// ValidBucket dit si un seau est déclaré.
func ValidBucket(bucket string) bool {
	if bucket == BucketRejected {
		return true
	}
	for _, known := range Buckets {
		if known == bucket {
			return true
		}
	}
	return false
}

// anomalyTrackers borne ce qui compte comme anomalie.
//
// L'instance en déclare deux — `Anomalie` et `Anomalie (SDN4)` — là où doc 04
// supposait un nom unique. Compter sur l'égalité à `Anomalie` en manquerait la
// moitié, et le préfixe est le seul point commun fiable.
const anomalyTrackerPrefix = "Anomalie"

// ListVersionSummaries rend les versions et leur avancement.
//
// Les versions closes sans aucune demande ni release ne sont pas masquées : elles
// existent dans Redmine, et les cacher ferait douter de l'exhaustivité de l'écran.
// ListVersionSummaries rend les versions **d'un projet**.
//
// Bornée au projet depuis que le dashboard en pilote plusieurs. Elle ne l'était
// pas : la route `/projects/{p}/versions` passait son projet, et la requête
// l'ignorait — un projet sans aucune version affichait les vingt du voisin, sans
// qu'aucune erreur ne le dise. C'est le pire mode de défaillance du
// multi-projet, celui où l'écran d'un client montre les données d'un autre.
func (db *DB) ListVersionSummaries(ctx context.Context, projectID int64) ([]VersionSummary, error) {
	rows, err := db.pool.Query(ctx, versionSummaryQuery+`
		   and v.project_id = $3
		 order by v.due_date desc nulls last, v.name desc`,
		anomalyTrackerPrefix, OpenBuckets, projectID)
	if err != nil {
		return nil, fmt.Errorf("liste des versions: %w", err)
	}
	defer rows.Close()

	var summaries []VersionSummary
	for rows.Next() {
		summary, err := scanVersionSummary(rows)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// GetVersionSummary rend une version par sa clé locale.
func (db *DB) GetVersionSummary(ctx context.Context, id int64) (VersionSummary, error) {
	rows, err := db.pool.Query(ctx, versionSummaryQuery+` and v.id = $3`,
		anomalyTrackerPrefix, OpenBuckets, id)
	if err != nil {
		return VersionSummary{}, fmt.Errorf("version %d: %w", id, err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return VersionSummary{}, fmt.Errorf("version %d: %w", id, err)
		}
		return VersionSummary{}, ErrVersionNotFoundByID
	}
	return scanVersionSummary(rows)
}

// versionSummaryQuery agrège demandes et releases par version.
//
// Les agrégats sont calculés en sous-requêtes latérales plutôt que par des
// jointures : joindre `issues` et `releases` dans la même requête multiplierait les
// lignes l'une par l'autre, et chaque compte serait faux d'un facteur égal au nombre
// de lignes de l'autre table.
const versionSummaryQuery = `
	select v.id, v.project_id, coalesce(v.external_id, ''), v.origin, v.name, v.status, v.due_date, v.synced_at,
	       counts.counted, counts.done, counts.weighted, counts.anomalies,
	       releases.total
	from versions v
	left join lateral (
	    select count(*) filter (where status_bucket <> 'rejected')            as counted,
	           count(*) filter (where status_bucket = 'done')                 as done,
	           coalesce(sum(done_ratio) filter (where status_bucket <> 'rejected'), 0) as weighted,
	           -- Ouverte au sens du seau projeté, jamais du drapeau is_closed de
	           -- Redmine : les deux divergent, et c'est ce qui faisait compter cinq
	           -- anomalies terminées comme ouvertes. (Pas de backtick ici : cette
	           -- chaîne est brute, un backtick la fermerait.)
	           count(*) filter (
	               where tracker like $1 || '%' and status_bucket = any($2)
	           ) as anomalies
	    from issues where version_id = v.id
	) counts on true
	left join lateral (
	    select count(*) as total from releases where version_id = v.id
	) releases on true
	where true`

func scanVersionSummary(rows pgx.Rows) (VersionSummary, error) {
	var summary VersionSummary
	var origin string
	if err := rows.Scan(&summary.ID, &summary.ProjectID, &summary.ExternalID, &origin, &summary.Name, &summary.Status,
		&summary.DueDate, &summary.SyncedAt, &summary.Counted, &summary.Done,
		&summary.WeightedDone, &summary.OpenAnomalies, &summary.Releases); err != nil {
		return VersionSummary{}, fmt.Errorf("lecture d'une version: %w", err)
	}
	// La provisoire se lit dans `origin` depuis la migration 0028. Elle se
	// déduisait auparavant du signe de `redmine_id` — un entier négatif qui
	// n'existait que pour tenir un `not null`, et qu'il fallait savoir lire.
	summary.Pending = origin == VersionOriginPending
	return summary, nil
}

// VersionIssue est une demande dans le périmètre d'une version.
type VersionIssue struct {
	// ExternalID est l'identifiant de la demande dans le suivi, en texte.
	ExternalID string
	Tracker    string
	Subject    string
	StatusName string
	Bucket     string
	Priority   string
	Assignee   string
	DoneRatio  int
	IsClosed   bool
	// Anomaly distingue les anomalies, comptées à part du taux de complétion.
	Anomaly bool
}

// ListVersionIssues rend le périmètre d'une version.
//
// Toutes les demandes, rejetées comprises : elles sortent du calcul de complétion
// mais restent le périmètre discuté, et les masquer ferait chercher une demande dont
// on sait qu'elle existe.
func (db *DB) ListVersionIssues(ctx context.Context, versionID int64) ([]VersionIssue, error) {
	rows, err := db.pool.Query(ctx,
		`select external_id, tracker, subject, status_name, status_bucket,
		        coalesce(priority, ''), coalesce(assignee, ''), done_ratio, is_closed
		 from issues
		 where version_id = $1
		 order by tracker, external_id`, versionID)
	if err != nil {
		return nil, fmt.Errorf("demandes de la version %d: %w", versionID, err)
	}
	defer rows.Close()

	var issues []VersionIssue
	for rows.Next() {
		var issue VersionIssue
		if err := rows.Scan(&issue.ExternalID, &issue.Tracker, &issue.Subject,
			&issue.StatusName, &issue.Bucket, &issue.Priority, &issue.Assignee,
			&issue.DoneRatio, &issue.IsClosed); err != nil {
			return nil, fmt.Errorf("demandes de la version %d: %w", versionID, err)
		}
		issue.Anomaly = len(issue.Tracker) >= len(anomalyTrackerPrefix) &&
			issue.Tracker[:len(anomalyTrackerPrefix)] == anomalyTrackerPrefix
		issues = append(issues, issue)
	}
	return issues, rows.Err()
}

// SyncState est l'état d'une synchronisation.
//
// `LastOK` est distinct de `LastRun` pour que l'interface puisse dire « données
// vieilles de quatorze minutes, dernière synchronisation en échec » plutôt qu'un
// chiffre muet.
type SyncState struct {
	Source    string
	Cursor    string
	LastRun   *time.Time
	LastOK    *time.Time
	LastError string
}

// GetSyncState rend l'état d'une source, ou un état vide si elle n'a jamais tourné.
func (db *DB) GetSyncState(ctx context.Context, source string) (SyncState, error) {
	state := SyncState{Source: source}
	var cursor, lastError *string

	err := db.pool.QueryRow(ctx,
		`select cursor, last_run, last_ok, last_error from sync_state where source = $1`,
		source).Scan(&cursor, &state.LastRun, &state.LastOK, &lastError)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Jamais synchronisé n'est pas une erreur : c'est l'état au premier
			// démarrage, et il conduit à une lecture complète.
			return state, nil
		}
		return SyncState{}, fmt.Errorf("état de synchronisation %q: %w", source, err)
	}
	if cursor != nil {
		state.Cursor = *cursor
	}
	if lastError != nil {
		state.LastError = *lastError
	}
	return state, nil
}

// RecordSyncSuccess avance le curseur et efface l'erreur précédente.
func (db *DB) RecordSyncSuccess(ctx context.Context, source, cursor string) error {
	_, err := db.pool.Exec(ctx,
		`insert into sync_state (source, cursor, last_run, last_ok, last_error, updated_at)
		 values ($1, nullif($2, ''), now(), now(), null, now())
		 on conflict (source) do update set
		     cursor = excluded.cursor, last_run = excluded.last_run,
		     last_ok = excluded.last_ok, last_error = null, updated_at = now()`,
		source, cursor)
	if err != nil {
		return fmt.Errorf("enregistrement de la synchronisation %q: %w", source, err)
	}
	return nil
}

// RecordSyncFailure note l'échec sans toucher au curseur.
//
// Le curseur est laissé en place à dessein : l'avancer après un échec sauterait la
// fenêtre non lue, et les demandes modifiées pendant celle-ci ne seraient jamais
// reprises. Mieux vaut relire deux fois que perdre.
func (db *DB) RecordSyncFailure(ctx context.Context, source, message string) error {
	_, err := db.pool.Exec(ctx,
		`insert into sync_state (source, last_run, last_error, updated_at)
		 values ($1, now(), $2, now())
		 on conflict (source) do update set
		     last_run = excluded.last_run, last_error = excluded.last_error,
		     updated_at = now()`,
		source, message)
	if err != nil {
		return fmt.Errorf("enregistrement de l'échec de synchronisation %q: %w", source, err)
	}
	return nil
}

// ResetSyncCursor efface le curseur pour forcer une lecture complète.
func (db *DB) ResetSyncCursor(ctx context.Context, source string) error {
	_, err := db.pool.Exec(ctx,
		`insert into sync_state (source, cursor, updated_at) values ($1, null, now())
		 on conflict (source) do update set cursor = null, updated_at = now()`, source)
	if err != nil {
		return fmt.Errorf("réinitialisation du curseur %q: %w", source, err)
	}
	return nil
}

// VersionRelease est une release produite pour une version.
type VersionRelease struct {
	Tag          string
	Kind         string
	State        string
	ImageVersion string
	CreatedAt    time.Time
}

// ListVersionReleases rend les releases d'une version, la plus récente d'abord.
func (db *DB) ListVersionReleases(ctx context.Context, versionID int64) ([]VersionRelease, error) {
	rows, err := db.pool.Query(ctx,
		`select tag, kind, state, image_version, created_at
		 from releases where version_id = $1
		 order by created_at desc`, versionID)
	if err != nil {
		return nil, fmt.Errorf("releases de la version %d: %w", versionID, err)
	}
	defer rows.Close()

	var releases []VersionRelease
	for rows.Next() {
		var item VersionRelease
		if err := rows.Scan(&item.Tag, &item.Kind, &item.State,
			&item.ImageVersion, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("releases de la version %d: %w", versionID, err)
		}
		releases = append(releases, item)
	}
	return releases, rows.Err()
}
