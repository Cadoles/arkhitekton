package store

import (
	"context"
	"fmt"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/release"
	"github.com/jackc/pgx/v5"
)

/*
 * Projection des versions et des demandes Redmine.
 *
 * La synchronisation est une interrogation périodique incrémentale, voie définitive
 * et non repli : aucun plugin webhook n'est déployable sur l'instance (B6).
 */

// TrackedVersion est une version telle que la synchronisation la dépose.
// Les origines d'une version, telles que la contrainte de la migration 0028 les
// énumère. Une version vient du suivi, a été posée en attendant que le suivi la
// connaisse, ou appartient à un projet qui n'en a pas.
const (
	VersionOriginTracker = "tracker"
	VersionOriginPending = "pending"
	VersionOriginOwn     = "own"
)

type TrackedVersion struct {
	// Tool nomme l'outil de suivi d'où vient cette version — `redmine`. Vide
	// quand elle n'en vient pas.
	Tool string
	// ExternalID est son identifiant **dans** cet outil, en texte : Redmine
	// numérote, Jira écrit `MSE-1234`.
	ExternalID string
	// ProjectID est le dépôt qui suit cette version. L'identifiant externe n'est
	// unique qu'en son sein : deux projets peuvent suivre deux instances, ou deux
	// projets de la même instance.
	ProjectID int64
	Name      string
	// Normalized est la forme comparable, calculée par `release.NormalizeVersionName`.
	// Elle n'est pas unique : `3.1.2` et `v3.1.2` peuvent coexister.
	Normalized  string
	Description string
	Status      string
	DueDate     *time.Time
	UpdatedOn   *time.Time
}

// TrackedIssue est une demande telle que la synchronisation la dépose.
type TrackedIssue struct {
	// ProjectID borne le rattachement à la version : deux projets suivis peuvent
	// porter le même identifiant Redmine.
	ProjectID int64
	// Tool nomme l'outil de suivi, ExternalID l'identifiant qu'il emploie.
	Tool       string
	ExternalID string
	// VersionExternalID est l'identifiant de la version **dans l'outil**, pas la
	// clé locale :
	// la synchronisation lit Redmine et ne connaît que ses identifiants.
	VersionExternalID *string
	Tracker           string
	Subject           string
	StatusName        string
	StatusBucket      string
	Priority          string
	Assignee          string
	DoneRatio         int
	IsClosed          bool
	UpdatedOn         *time.Time
}

// UpsertRedmineVersion enregistre une version et rend sa clé locale.
//
// Simple, et volontairement : l'identité est le couple `(tool, external_id)`. Le rapprochement avec
// les versions provisoires est une opération distincte ([DB.AdoptPendingVersions]),
// exécutée après que toutes les versions ont été déposées.
//
// Les mêler serait dépendant de l'ordre d'itération. `3.1.2` et `v3.1.2` se
// réduisent au même numéro ; celle traitée en premier adopterait la provisoire, et
// une release se retrouverait rattachée à la version close plutôt qu'à celle qui est
// ouverte — un arbitraire invisible.
func (db *DB) UpsertRedmineVersion(ctx context.Context, version TrackedVersion) (int64, error) {
	var id int64
	err := db.pool.QueryRow(ctx,
		`insert into versions
		     (project_id, tool, external_id, name, normalized_name, description, status, due_date,
		      external_updated_on, origin, synced_at)
		 values ($1, $2, $3, $4, $5, nullif($6, ''), $7, $8, $9, 'tracker', now())
		 -- Le conflit porte sur le triplet : le même identifiant peut être suivi
		 -- par deux projets, ou par deux outils, et une unicité plus large les
		 -- ferait s'écraser.
		 on conflict (project_id, tool, external_id) where external_id is not null do update set
		     name = excluded.name,
		     normalized_name = excluded.normalized_name,
		     description = excluded.description,
		     status = excluded.status,
		     due_date = excluded.due_date,
		     external_updated_on = excluded.external_updated_on,
		     origin = 'tracker',
		     synced_at = now(),
		     updated_at = now()
		 returning id`,
		version.ProjectID, version.Tool, version.ExternalID, version.Name, version.Normalized,
		version.Description, version.Status, version.DueDate, version.UpdatedOn).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("enregistrement de la version %s/%s: %w",
			version.Tool, version.ExternalID, err)
	}
	return id, nil
}

// Adoption rend compte du rapprochement d'une version provisoire.
type Adoption struct {
	// PendingName est le nom porté par la provisoire, issu du tag de la release.
	PendingName string
	// AdoptedInto est le nom de la version Redmine qui l'a absorbée. Vide quand
	// l'adoption n'a pas eu lieu.
	AdoptedInto string
	// Releases est le nombre de releases rattachées à la version réelle.
	Releases int64
	// Ambiguous marque une provisoire que plusieurs versions réelles revendiquent.
	Ambiguous bool
	// Candidates liste ces versions, pour que l'écran d'administration les nomme.
	Candidates []string
}

// AdoptPendingVersions rapproche les versions provisoires des versions réelles.
//
// `EnsureVersion` crée, à la création d'une release, une ligne d'origine `pending`
// quand la version n'est pas encore connue — il faut bien rattacher la release à
// quelque chose. Sans rapprochement, la version réelle s'afficherait sans aucune
// release et la provisoire sans aucun périmètre.
//
// **L'adoption exige une candidate unique.** Quand plusieurs versions portent le même
// numéro, la version non close est préférée : les seize versions closes du projet
// viennent d'un ancien système de versionnement, et deux de leurs numéros ont été
// repris par la convention courante — une release visant ce numéro vise la version
// active, jamais son homonyme historique.
//
// Si l'indétermination subsiste — plusieurs actives, ou plusieurs closes et aucune
// active — rien n'est adopté. Rattacher à la mauvaise version est pire que de ne rien
// rattacher, et le cas est rendu à l'appelant pour affichage.
//
// À appeler après [DB.UpsertRedmineVersion] sur l'ensemble des versions : c'est ce qui
// rend le résultat indépendant de l'ordre de lecture.
func (db *DB) AdoptPendingVersions(ctx context.Context) ([]Adoption, error) {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("adoption: %w", err)
	}
	defer tx.Rollback(ctx)

	// `for update` sur les provisoires : deux synchronisations concurrentes ne
	// doivent pas adopter la même deux fois.
	rows, err := tx.Query(ctx,
		// `origin = 'pending'` et non un identifiant négatif : les deux coïncidaient
		// tant qu'une version sans identifiant de suivi ne pouvait être qu'en
		// attente. Un projet sans suivi en produit désormais qui sont
		// **définitives**, et tenter de les adopter les rapprocherait d'un outil
		// qui n'existe pas.
		`select id, project_id, name, normalized_name from versions
		 where origin = 'pending' order by id for update`)
	if err != nil {
		return nil, fmt.Errorf("lecture des versions provisoires: %w", err)
	}
	type pending struct {
		id        int64
		projectID int64
		name      string
		// normalized est la forme comparable. Le rapprochement se fait **dans le
		// projet** : deux applications suivies ensemble peuvent porter le même
		// numéro, et adopter au travers rattacherait une release à la version de
		// l'autre — silencieusement, puisque les deux existent.
		normalized *string
	}
	var pendings []pending
	for rows.Next() {
		var entry pending
		if err := rows.Scan(&entry.id, &entry.projectID, &entry.name, &entry.normalized); err != nil {
			rows.Close()
			return nil, fmt.Errorf("lecture des versions provisoires: %w", err)
		}
		pendings = append(pendings, entry)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("lecture des versions provisoires: %w", err)
	}

	var report []Adoption
	for _, entry := range pendings {
		if entry.normalized == nil || *entry.normalized == "" {
			continue
		}

		candidates, err := realVersionsNamed(ctx, tx, entry.projectID, *entry.normalized)
		if err != nil {
			return nil, err
		}

		// Plusieurs versions peuvent porter le même numéro, l'une close et l'autre
		// ouverte : les seize versions closes du projet viennent d'un ancien système
		// de versionnement, et deux de leurs numéros ont été repris par la
		// convention courante. Une release visant ce numéro vise la version encore
		// active, jamais son homonyme historique.
		//
		// La préférence porte sur « non close » et non sur « ouverte », parce qu'une
		// version se clôt normalement après sa livraison : figer la règle sur
		// `open` ferait cesser de fonctionner un rapprochement qui marchait, le jour
		// où la version visée se clôt.
		retained := candidates
		if len(candidates) > 1 {
			if active := activeOnly(candidates); len(active) == 1 {
				retained = active
			}
		}

		switch len(retained) {
		case 0:
			// Aucune version réelle ne porte ce numéro. La provisoire reste, et
			// `PendingVersions` la signalera : c'est le symptôme d'une faute de
			// frappe dans un tag, ou d'une version absente de Redmine.
		case 1:
			moved, err := mergeVersion(ctx, tx, entry.id, retained[0].id)
			if err != nil {
				return nil, err
			}
			report = append(report, Adoption{
				PendingName: entry.name, AdoptedInto: retained[0].name, Releases: moved,
			})
		default:
			// Toujours indécidable : plusieurs versions actives, ou plusieurs closes
			// et aucune active. Trancher au hasard rattacherait des releases à la
			// mauvaise version.
			names := make([]string, 0, len(retained))
			for _, candidate := range retained {
				names = append(names, candidate.name)
			}
			report = append(report, Adoption{
				PendingName: entry.name, Ambiguous: true, Candidates: names,
			})
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("adoption: %w", err)
	}
	return report, nil
}

type versionRef struct {
	id     int64
	name   string
	status string
}

// activeOnly retient les versions non closes.
//
// `locked` en fait partie : une version verrouillée n'accepte plus de nouvelle
// demande mais n'est pas terminée pour autant.
func activeOnly(candidates []versionRef) []versionRef {
	active := make([]versionRef, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.status != "closed" {
			active = append(active, candidate)
		}
	}
	return active
}

// realVersionsNormalized est [realVersionsNamed] hors transaction, pour les
// lectures qui n'ont pas de provisoire à verrouiller — le rattachement immédiat
// d'une release, notamment.
func (db *DB) realVersionsNormalized(ctx context.Context, projectID int64, normalized string) ([]versionRef, error) {
	rows, err := db.pool.Query(ctx,
		`select id, name, status from versions
		 where project_id = $1 and origin = 'tracker' and normalized_name = $2
		 order by id`, projectID, normalized)
	if err != nil {
		return nil, fmt.Errorf("versions réelles nommées %q: %w", normalized, err)
	}
	defer rows.Close()
	return scanVersionRefs(rows, normalized)
}

func realVersionsNamed(
	ctx context.Context, tx pgx.Tx, projectID int64, normalized string,
) ([]versionRef, error) {
	rows, err := tx.Query(ctx,
		`select id, name, status from versions
		 where project_id = $1 and origin = 'tracker' and normalized_name = $2
		 order by id`, projectID, normalized)
	if err != nil {
		return nil, fmt.Errorf("versions réelles nommées %q: %w", normalized, err)
	}
	defer rows.Close()
	return scanVersionRefs(rows, normalized)
}

func scanVersionRefs(rows pgx.Rows, normalized string) ([]versionRef, error) {
	var refs []versionRef
	for rows.Next() {
		var ref versionRef
		if err := rows.Scan(&ref.id, &ref.name, &ref.status); err != nil {
			return nil, fmt.Errorf("versions réelles nommées %q: %w", normalized, err)
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

// mergeVersion déplace les releases d'une provisoire vers la version réelle, puis
// supprime la provisoire.
//
// L'ordre importe : supprimer d'abord mettrait `releases.version_id` à null par la
// clé étrangère, et le rattachement serait perdu au lieu d'être transféré.
func mergeVersion(ctx context.Context, tx pgx.Tx, pendingID, realID int64) (int64, error) {
	tag, err := tx.Exec(ctx,
		`update releases set version_id = $2 where version_id = $1`, pendingID, realID)
	if err != nil {
		return 0, fmt.Errorf("transfert des releases de la version %d: %w", pendingID, err)
	}

	if _, err := tx.Exec(ctx, `delete from versions where id = $1`, pendingID); err != nil {
		return 0, fmt.Errorf("suppression de la version provisoire %d: %w", pendingID, err)
	}
	return tag.RowsAffected(), nil
}

// UpsertRedmineIssue enregistre une demande.
//
// Le rattachement se fait par sous-requête sur `external_id` : la synchronisation ne
// connaît que les identifiants du suivi, et résoudre la clé locale côté Go
// demanderait un aller-retour par demande.
//
// Une version inconnue laisse `version_id` nul plutôt que de faire échouer la
// demande. Cela arrive normalement : une demande peut viser une version d'un autre
// projet, ou une version créée entre la lecture des versions et celle des demandes.
//
// Le rattachement est **borné au projet**. Deux projets suivis peuvent porter le
// même identifiant Redmine — deux instances, ou deux projets d'une même instance —
// et une sous-requête sans cette borne rattacherait la demande à la version de
// l'autre.
func (db *DB) UpsertRedmineIssue(ctx context.Context, issue TrackedIssue) error {
	_, err := db.pool.Exec(ctx,
		`insert into issues
		     (project_id, tool, external_id, version_id, tracker, subject,
		      status_name, status_bucket, priority, assignee, done_ratio, is_closed,
		      external_updated_on, synced_at)
		 values ($12, $13, $1,
		         (select id from versions
		          where external_id = $2 and project_id = $12),
		         $3, $4, $5, $6, nullif($7, ''), nullif($8, ''), $9, $10, $11, now())
		 -- Le conflit porte sur le triplet : deux projets, ou deux outils, peuvent
		 -- numéroter pareil sans désigner la même demande.
		 on conflict (project_id, tool, external_id) do update set
		     version_id = excluded.version_id,
		     tracker = excluded.tracker,
		     subject = excluded.subject,
		     status_name = excluded.status_name,
		     status_bucket = excluded.status_bucket,
		     priority = excluded.priority,
		     assignee = excluded.assignee,
		     done_ratio = excluded.done_ratio,
		     is_closed = excluded.is_closed,
		     external_updated_on = excluded.external_updated_on,
		     synced_at = now(),
		     updated_at = now()`,
		issue.ExternalID, issue.VersionExternalID, issue.Tracker, issue.Subject,
		issue.StatusName, issue.StatusBucket, issue.Priority, issue.Assignee,
		issue.DoneRatio, issue.IsClosed, issue.UpdatedOn, issue.ProjectID, issue.Tool)
	if err != nil {
		return fmt.Errorf("enregistrement de la demande %s/%s: %w",
			issue.Tool, issue.ExternalID, err)
	}
	return nil
}

// StatusMapping rend la projection statut → seau d'un projet.
//
// Chargée en bloc et non interrogée par demande : trente et un statuts tiennent en
// mémoire, et une requête par demande multiplierait les allers-retours par le nombre
// de demandes synchronisées.
//
// Superposée depuis la migration 0043 : un **défaut d'instance** — les lignes
// sans projet, posées par l'amorçage — et les **écarts** d'un projet, qui
// l'emportent.
//
// Les deux, et non l'un ou l'autre. Une projection strictement par projet ferait
// naître chaque projet sans aucune règle, et son taux de complétion compterait
// toutes ses demandes comme non commencées. Une projection strictement commune
// ferait qu'un statut déclaré pour un client déplacerait un chiffre affiché à un
// autre — « En revue » ne veut pas dire la même chose chez deux clients.
//
// `distinct on` retient la ligne du projet quand elle existe : l'ordre place les
// lignes non nulles en tête, et PostgreSQL garde la première de chaque nom.
func (db *DB) StatusMapping(ctx context.Context, projectID int64) (map[string]string, error) {
	rows, err := db.pool.Query(ctx,
		`select distinct on (status_name) status_name, bucket
		 from issue_status_mapping
		 where project_id = $1 or project_id is null
		 order by status_name, project_id nulls last`,
		projectID)
	if err != nil {
		return nil, fmt.Errorf("projection des statuts: %w", err)
	}
	defer rows.Close()

	mapping := map[string]string{}
	for rows.Next() {
		var name, bucket string
		if err := rows.Scan(&name, &bucket); err != nil {
			return nil, fmt.Errorf("projection des statuts: %w", err)
		}
		mapping[name] = bucket
	}
	return mapping, rows.Err()
}

// UnmappedStatus est un statut rencontré sans projection déclarée.
type UnmappedStatus struct {
	StatusName string
	Issues     int
}

// UnmappedStatuses rend les statuts tombés dans le seau par défaut.
//
// Un statut inconnu n'est pas ignoré en silence : il atterrit dans `todo` et
// apparaît ici, donc dans l'écran d'administration. Les statuts Redmine sont propres
// au workflow du client et changent sans prévenir ; un ajout non projeté ferait
// dériver le taux de complétion sans que rien ne le signale.
func (db *DB) UnmappedStatuses(ctx context.Context, projectID int64) ([]UnmappedStatus, error) {
	// Un statut est projeté s'il l'est **pour ce projet ou par défaut**. Ne
	// regarder que les lignes du projet signalerait comme non projetés les trente
	// et un statuts que l'amorçage couvre, et l'écran crierait sans raison — un
	// écran d'administration qui crie sans raison cesse d'être lu.
	rows, err := db.pool.Query(ctx,
		`select i.status_name, count(*)
		 from issues i
		 left join issue_status_mapping m
		        on m.status_name = i.status_name
		       and (m.project_id = i.project_id or m.project_id is null)
		 where m.status_name is null and i.project_id = $1
		 group by i.status_name
		 order by count(*) desc, i.status_name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("statuts non projetés: %w", err)
	}
	defer rows.Close()

	var unmapped []UnmappedStatus
	for rows.Next() {
		var entry UnmappedStatus
		if err := rows.Scan(&entry.StatusName, &entry.Issues); err != nil {
			return nil, fmt.Errorf("statuts non projetés: %w", err)
		}
		unmapped = append(unmapped, entry)
	}
	return unmapped, rows.Err()
}

// SetStatusBucket déclare ou corrige la projection d'un statut dans un projet.
//
// Écrit toujours une ligne **du projet**, jamais le défaut de l'instance : une
// décision prise sur un écran de projet ne doit pas déplacer un chiffre affiché
// à un autre client.
func (db *DB) SetStatusBucket(ctx context.Context, projectID int64, statusName, bucket string) error {
	_, err := db.pool.Exec(ctx,
		// Le prédicat accompagne la cible du conflit : l'unicité par projet est un
		// index **partiel**, et PostgreSQL ne l'infère qu'à cette condition. Sans
		// lui, l'écriture échoue sur « no unique or exclusion constraint », un
		// message qui ne désigne pas la cause.
		`insert into issue_status_mapping (project_id, status_name, bucket)
		 values ($1, $2, $3)
		 on conflict (project_id, status_name) where project_id is not null
		 do update set bucket = excluded.bucket, updated_at = now()`,
		projectID, statusName, bucket)
	if err != nil {
		return fmt.Errorf("projection du statut %q: %w", statusName, err)
	}
	return nil
}

// ReprojectIssues réapplique la projection aux demandes déjà enregistrées.
//
// Nécessaire après une correction du mapping : sans elle, la nouvelle règle ne
// vaudrait que pour les demandes modifiées ensuite dans Redmine, et l'écran
// afficherait un mélange des deux règles sans le dire. Rend le nombre de demandes
// déplacées.
func (db *DB) ReprojectIssues(ctx context.Context, projectID int64) (int64, error) {
	// L'auto-jointure permet la jointure externe sur la table mise à jour :
	// PostgreSQL n'accepte pas un `left join` directement sur la cible d'un
	// `update`. Le `coalesce` porte la règle du seau par défaut, la même qu'à
	// l'insertion — la diviser en deux endroits la ferait diverger.
	//
	// Bornée au projet : réappliquer la projection de l'un ne doit pas toucher
	// aux demandes de l'autre, dont le vocabulaire n'est pas le même.
	//
	// La superposition est celle de [DB.StatusMapping], reprise dans un
	// sous-select : la règle appliquée ici doit être **exactement** celle que
	// l'écran affiche, sinon la réapplication déplacerait des demandes vers des
	// seaux que personne n'a déclarés.
	tag, err := db.pool.Exec(ctx,
		`update issues i
		 set status_bucket = coalesce(m.bucket, 'todo'), updated_at = now()
		 from issues source
		 left join (
		     select distinct on (status_name) status_name, bucket
		     from issue_status_mapping
		     where project_id = $1 or project_id is null
		     order by status_name, project_id nulls last
		 ) m on m.status_name = source.status_name
		 where source.id = i.id
		   and i.project_id = $1
		   and i.status_bucket <> coalesce(m.bucket, 'todo')`, projectID)
	if err != nil {
		return 0, fmt.Errorf("réapplication de la projection: %w", err)
	}
	return tag.RowsAffected(), nil
}

// AmbiguousVersion signale plusieurs versions réduites au même numéro.
type AmbiguousVersion struct {
	Normalized string
	Names      []string
}

// AmbiguousVersions rend les numéros portés par plusieurs versions **actives**.
//
// Le filtre sur les actives est ce qui rend cet écran utile. Un numéro repris
// — `3.1.2` close et `v3.1.2` ouverte — se tranche en faveur de la version
// active. Les signaler serait deux avertissements permanents pour une
// situation qui n'exige aucune décision, et un écran d'administration qui crie sans
// raison cesse d'être lu.
//
// Ne subsistent donc que les cas réellement indécidables : deux versions actives
// portant le même numéro.
func (db *DB) AmbiguousVersions(ctx context.Context, projectID int64) ([]AmbiguousVersion, error) {
	// Bornée au projet : deux projets peuvent légitimement porter chacun une
	// `3.1.0`, et les regrouper inventerait une ambiguïté qui n'existe pas.
	rows, err := db.pool.Query(ctx,
		`select normalized_name, array_agg(name order by name)
		 from versions
		 where normalized_name is not null and origin = 'tracker' and status <> 'closed'
		   and project_id = $1
		 group by normalized_name
		 having count(*) > 1
		 order by normalized_name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("versions ambiguës: %w", err)
	}
	defer rows.Close()

	var ambiguous []AmbiguousVersion
	for rows.Next() {
		var entry AmbiguousVersion
		if err := rows.Scan(&entry.Normalized, &entry.Names); err != nil {
			return nil, fmt.Errorf("versions ambiguës: %w", err)
		}
		ambiguous = append(ambiguous, entry)
	}
	return ambiguous, rows.Err()
}

// PendingVersions rend les versions provisoires jamais adoptées.
//
// Une provisoire qui subsiste après une synchronisation réussie désigne une release
// rattachée à une version que Redmine ne connaît pas — faute de frappe dans le tag,
// version supprimée, ou ambiguïté de nommage. Le signaler évite qu'un écran affiche
// « en attente de synchronisation » indéfiniment sans que personne ne sache pourquoi.
func (db *DB) PendingVersions(ctx context.Context, projectID int64) ([]string, error) {
	rows, err := db.pool.Query(ctx,
		`select name from versions
		 where origin = 'pending' and project_id = $1 order by name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("versions provisoires: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("versions provisoires: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// RenormalizeVersions réaligne la colonne normalisée sur la règle en vigueur.
//
// `versions.normalized_name` est une colonne **dérivée**, écrite au moment de la
// synchronisation. Changer la règle rend donc obsolètes toutes les lignes déjà
// écrites, et le rapprochement se ferait alors entre une forme calculée avec la
// nouvelle règle et une forme stockée avec l'ancienne : une release cesserait
// de trouver sa version, sans le moindre message.
//
// La synchronisation Redmine finirait par les réécrire, mais seulement à son
// prochain passage, et seulement pour les versions qu'elle voit encore. Le faire
// au démarrage ferme cette fenêtre et rattrape aussi les provisoires, qu'aucune
// synchronisation ne recalcule.
//
// Rend le nombre de lignes corrigées, pour que le démarrage puisse le dire : zéro
// est le cas courant, et un nombre non nul mérite d'apparaître au journal parce
// qu'il signale un changement de convention.
func (db *DB) RenormalizeVersions(ctx context.Context) (int, error) {
	rows, err := db.pool.Query(ctx, `select id, name, normalized_name from versions`)
	if err != nil {
		return 0, fmt.Errorf("lecture des versions à renormaliser: %w", err)
	}

	type correction struct {
		id         int64
		normalized string
	}
	var corrections []correction

	for rows.Next() {
		var id int64
		var name, normalized string
		if err := rows.Scan(&id, &name, &normalized); err != nil {
			rows.Close()
			return 0, fmt.Errorf("lecture d'une version à renormaliser: %w", err)
		}
		if wanted := release.NormalizeVersionName(name); wanted != normalized {
			corrections = append(corrections, correction{id: id, normalized: wanted})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("lecture des versions à renormaliser: %w", err)
	}

	// Les corrections sont appliquées après la lecture complète, et non pendant :
	// écrire sur la connexion qui porte le curseur d'une autre requête n'est pas
	// permis par pgx, et le découvrir à l'exécution serait une panne au démarrage.
	for _, fix := range corrections {
		if _, err := db.pool.Exec(ctx,
			`update versions set normalized_name = $2 where id = $1`,
			fix.id, fix.normalized); err != nil {
			return 0, fmt.Errorf("renormalisation de la version %d: %w", fix.id, err)
		}
	}
	return len(corrections), nil
}
