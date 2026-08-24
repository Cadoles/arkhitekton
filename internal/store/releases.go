package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/release"
)

// Release est une release enregistrée.
type Release struct {
	ID        int64
	VersionID int64
	// Tag est l'identité de la release : `1.8.0-rc2`, `1.8.0`.
	Tag   string
	Kind  string
	State string
	// ImageVersion est la version portée par l'image, distincte du tag pour
	// toute release finale produite par re-tag : ce qui part en production est
	// bit pour bit ce qui a été validé, et son `version.txt` reste celui de la
	// RC (B17). Comparer PortalData au tag échouerait systématiquement.
	ImageVersion    string
	SourceReleaseID *int64
	// GitRef est la référence désignée à la création — `main`, un tag. Elle dit
	// d'où la release est partie ; elle ne suffit pas à la reproduire.
	GitRef string
	// GitCommit est la résolution de `GitRef` à cet instant. C'est lui qui rend la
	// release reproductible : une branche bouge (B17).
	GitCommit  string
	CreatedBy  string
	CreatedAt  time.Time
	ReleasedAt *time.Time
	// ManualActions est le récit libre des gestes manuels de la livraison, repris
	// tel quel dans le bon de livraison. L'auteur et la date l'accompagnent parce
	// que ce document est contractuel.
	ManualActions          string
	ManualActionsUpdatedBy string
	ManualActionsUpdatedAt *time.Time
}

// ErrReleaseNotFound signale une release absente.
var ErrReleaseNotFound = errors.New("release introuvable")

// ErrVersionNotFound signale une version Redmine absente.
//
// Distincte d'une erreur d'écriture : sans le lot 1, la table des versions est
// vide, et l'appelant doit pouvoir dire *pourquoi* la création est refusée plutôt
// que d'afficher une violation de contrainte.
var ErrVersionNotFound = errors.New("version introuvable")

// FindVersionByName retrouve une version Redmine par son nom.
//
// C'est le point de jointure de l'invariant : le tag d'une release est le nom de
// la version, éventuellement suivi d'un suffixe de pré-release (B17).
func (db *DB) FindVersionByName(ctx context.Context, name string) (int64, error) {
	var id int64
	err := db.pool.QueryRow(ctx, `select id from versions where name = $1`, name).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrVersionNotFound
		}
		return 0, fmt.Errorf("recherche de la version %q: %w", name, err)
	}
	return id, nil
}

// CreateRelease enregistre une release.
func (db *DB) CreateRelease(ctx context.Context, release Release) (int64, error) {
	var id int64
	err := db.pool.QueryRow(ctx,
		`insert into releases (
		     version_id, tag, kind, state, git_ref, git_commit, image_version,
		     source_release_id, created_by
		 ) values ($1, $2, $3, $4, $5, nullif($6, ''), $7, $8, $9)
		 returning id`,
		release.VersionID, release.Tag, release.Kind, release.State,
		release.GitRef, release.GitCommit, release.ImageVersion,
		release.SourceReleaseID, release.CreatedBy).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("création de la release %q: %w", release.Tag, err)
	}
	return id, nil
}

// GetRelease rend une release par son tag.
func (db *DB) GetRelease(ctx context.Context, tag string) (Release, error) {
	var release Release
	err := db.pool.QueryRow(ctx,
		`select id, version_id, tag, kind, state, coalesce(git_ref, ''),
		        coalesce(git_commit, ''), image_version,
		        source_release_id, created_by, created_at, released_at,
		        coalesce(manual_actions_md, ''), coalesce(manual_actions_updated_by, ''),
		        manual_actions_updated_at
		 from releases where tag = $1`, tag).
		Scan(&release.ID, &release.VersionID, &release.Tag, &release.Kind, &release.State,
			&release.GitRef, &release.GitCommit, &release.ImageVersion, &release.SourceReleaseID,
			&release.CreatedBy, &release.CreatedAt, &release.ReleasedAt,
			&release.ManualActions, &release.ManualActionsUpdatedBy, &release.ManualActionsUpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return release, ErrReleaseNotFound
		}
		return release, fmt.Errorf("lecture de la release %q: %w", tag, err)
	}
	return release, nil
}

// ListReleases rend les releases les plus récentes d'abord.
func (db *DB) ListReleases(ctx context.Context, limit int) ([]Release, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	rows, err := db.pool.Query(ctx,
		`select id, version_id, tag, kind, state, coalesce(git_ref, ''),
		        coalesce(git_commit, ''), image_version,
		        source_release_id, created_by, created_at, released_at
		 from releases order by created_at desc limit $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("liste des releases: %w", err)
	}
	defer rows.Close()

	var releases []Release
	for rows.Next() {
		var release Release
		if err := rows.Scan(&release.ID, &release.VersionID, &release.Tag, &release.Kind,
			&release.State, &release.GitRef, &release.GitCommit, &release.ImageVersion,
			&release.SourceReleaseID,
			&release.CreatedBy, &release.CreatedAt, &release.ReleasedAt); err != nil {
			return nil, fmt.Errorf("liste des releases: %w", err)
		}
		releases = append(releases, release)
	}
	return releases, rows.Err()
}

// SetReleaseState fait avancer une release.
//
// La transition autorisée est décidée par `internal/release`, hors de la base :
// une table de transitions dupliquée en SQL et en Go finirait par diverger, et
// c'est la version Go qui porte les messages d'erreur.
func (db *DB) SetReleaseState(ctx context.Context, id int64, state string) error {
	_, err := db.pool.Exec(ctx,
		`update releases set state = $2, updated_at = now() where id = $1`, id, state)
	if err != nil {
		return fmt.Errorf("état de la release %d: %w", id, err)
	}
	return nil
}

// Promotion est un tag de promotion poussé pour un environnement.
type Promotion struct {
	ReleaseID     int64
	EnvironmentID string
	ImageName     string
	Tag           string
	Digest        string
	JobID         *int64
	PushedAt      time.Time
}

// RecordPromotion enregistre une promotion demandée.
//
// « Demandée » et non « effectuée » : pousser le tag ne déploie rien. Flux
// constate ensuite, et c'est l'observation des environnements qui dira si la
// version est réellement livrée. Enregistrer ici ce qui a été **demandé** est ce
// qui permet plus tard de distinguer « promu mais pas déployé » de « jamais
// promu » (B7).
func (db *DB) RecordPromotion(ctx context.Context, promotion Promotion) error {
	_, err := db.pool.Exec(ctx,
		`insert into image_promotions (
		     release_id, environment_id, image_name, tag, digest, pushed_at, job_id
		 ) values ($1, $2, $3, $4, nullif($5, ''), now(), $6)
		 -- Repromouvoir un environnement est légitime : reprise après échec, ou
		 -- correction. La dernière demande fait foi.
		 on conflict (release_id, environment_id, image_name) do update set
		     tag = excluded.tag,
		     digest = coalesce(excluded.digest, image_promotions.digest),
		     pushed_at = excluded.pushed_at,
		     job_id = excluded.job_id`,
		promotion.ReleaseID, promotion.EnvironmentID, promotion.ImageName,
		promotion.Tag, promotion.Digest, promotion.JobID)
	if err != nil {
		return fmt.Errorf("enregistrement de la promotion de %q: %w", promotion.Tag, err)
	}
	return nil
}

// ListPromotions rend les promotions d'une release.
func (db *DB) ListPromotions(ctx context.Context, releaseID int64) ([]Promotion, error) {
	rows, err := db.pool.Query(ctx,
		`select environment_id, image_name, tag, coalesce(digest, ''), pushed_at, job_id
		 from image_promotions where release_id = $1 order by pushed_at desc`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("promotions de la release %d: %w", releaseID, err)
	}
	defer rows.Close()

	var promotions []Promotion
	for rows.Next() {
		promotion := Promotion{ReleaseID: releaseID}
		if err := rows.Scan(&promotion.EnvironmentID, &promotion.ImageName, &promotion.Tag,
			&promotion.Digest, &promotion.PushedAt, &promotion.JobID); err != nil {
			return nil, fmt.Errorf("promotions de la release %d: %w", releaseID, err)
		}
		promotions = append(promotions, promotion)
	}
	return promotions, rows.Err()
}

// EnsureVersion rattache une release à sa version, en la créant au besoin.
//
// La recherche se fait en trois temps, et l'ordre porte la sémantique :
//
//  1. le nom exact — le cas d'une provisoire déjà créée pour ce même numéro ;
//  2. le nom **normalisé**, en préférant la version non close — le cas nominal
//     depuis la synchronisation : une release taguée `3.2.0-rc1` a pour base
//     `3.2.0`, alors que la version Redmine s'appelle `v3.2.0`. Chercher le nom
//     exact seul créait une provisoire fantôme, adoptée cinq minutes plus tard
//     par la synchronisation ; entre-temps l'écran montrait la release accrochée
//     à une version « inconnue de Redmine » qui existait bel et bien. La règle de
//     préférence est celle de l'adoption, pour que le rattachement immédiat et le
//     rattachement différé désignent la même version ;
//  3. la création d'une provisoire à `redmine_id` négatif — le repli quand
//     Redmine ne connaît pas encore le numéro, reconnaissable et adopté plus tard.
func (db *DB) EnsureVersion(ctx context.Context, name string) (int64, error) {
	id, err := db.FindVersionByName(ctx, name)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, ErrVersionNotFound) {
		return 0, err
	}

	// Parmi les versions réelles portant ce numéro, la non-close l'emporte ; à
	// égalité le rattachement est indécidable et on retombe sur la provisoire —
	// choisir au hasard rattacherait la release à la mauvaise version, et la
	// provisoire, elle, reste visible et signalée.
	candidates, err := db.realVersionsNormalized(ctx, release.NormalizeVersionName(name))
	if err != nil {
		return 0, err
	}
	if active := activeOnly(candidates); len(active) == 1 {
		return active[0].id, nil
	}
	if len(candidates) == 1 {
		return candidates[0].id, nil
	}

	// `normalized_name` est renseigné dès la création, et ce n'est pas cosmétique :
	// c'est la seule colonne sur laquelle la synchronisation peut reconnaître cette
	// provisoire pour l'adopter. Nulle, la provisoire resterait orpheline et la
	// version réelle s'afficherait sans aucune release.
	err = db.pool.QueryRow(ctx,
		`insert into versions (redmine_id, name, normalized_name, status, synced_at)
		 values (-nextval('versions_id_seq'), $1, $2, 'en-attente-de-synchronisation', now())
		 returning id`, name, release.NormalizeVersionName(name)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("création de la version provisoire %q: %w", name, err)
	}
	return id, nil
}

// EnvironmentPromotion est la dernière promotion demandée pour un environnement.
type EnvironmentPromotion struct {
	EnvironmentID string
	ReleaseTag    string
	Tag           string
	PushedAt      time.Time
	JobID         *int64
}

// LatestPromotions rend, par environnement, la dernière promotion demandée.
//
// C'est la moitié manquante de l'observation : celle-ci dit ce qui tourne,
// celle-là ce qu'on a demandé. Sans les deux, « promu mais pas encore déployé »
// et « jamais promu » s'affichent à l'identique — or le premier cas se règle en
// attendant, le second en agissant (B7, B8).
func (db *DB) LatestPromotions(ctx context.Context) (map[string]EnvironmentPromotion, error) {
	rows, err := db.pool.Query(ctx,
		`select distinct on (p.environment_id)
		        p.environment_id, r.tag, p.tag, p.pushed_at, p.job_id
		 from image_promotions p
		 join releases r on r.id = p.release_id
		 order by p.environment_id, p.pushed_at desc`)
	if err != nil {
		return nil, fmt.Errorf("dernières promotions: %w", err)
	}
	defer rows.Close()

	latest := make(map[string]EnvironmentPromotion)
	for rows.Next() {
		var promotion EnvironmentPromotion
		if err := rows.Scan(&promotion.EnvironmentID, &promotion.ReleaseTag,
			&promotion.Tag, &promotion.PushedAt, &promotion.JobID); err != nil {
			return nil, fmt.Errorf("dernières promotions: %w", err)
		}
		latest[promotion.EnvironmentID] = promotion
	}
	return latest, rows.Err()
}

// SetManualActions enregistre le récit des gestes manuels d'une livraison.
//
// L'auteur et l'horodatage sont posés ici et non fournis par l'appelant : ce
// texte finit dans un document contractuel, et sa provenance ne doit pas dépendre
// de ce qu'un client veut bien déclarer.
func (db *DB) SetManualActions(ctx context.Context, tag, markdown, author string) error {
	tally, err := db.pool.Exec(ctx,
		`update releases
		 set manual_actions_md = nullif($2, ''),
		     manual_actions_updated_by = $3,
		     manual_actions_updated_at = now(),
		     updated_at = now()
		 where tag = $1`, tag, markdown, author)
	if err != nil {
		return fmt.Errorf("actions manuelles de %q: %w", tag, err)
	}
	if tally.RowsAffected() == 0 {
		return ErrReleaseNotFound
	}
	return nil
}

// ReleaseJob est un job rattaché à une release.
type ReleaseJob struct {
	ID            int64
	Kind          string
	State         string
	EnvironmentID string
	RunName       string
	RequestedBy   string
	RequestedAt   time.Time
	FinishedAt    *time.Time
}

// ListReleaseJobs rend ce qui a été déclenché pour une release.
//
// C'est le chemin entre une promotion et sa preuve : la promotion dit ce qui a
// été demandé, le job dit ce qui s'est passé, et son run porte les journaux.
func (db *DB) ListReleaseJobs(ctx context.Context, releaseID int64) ([]ReleaseJob, error) {
	rows, err := db.pool.Query(ctx,
		`select id, kind, state, coalesce(environment_id, ''), coalesce(run_name, ''),
		        requested_by, requested_at, finished_at
		 from jobs where release_id = $1 order by requested_at desc`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("jobs de la release %d: %w", releaseID, err)
	}
	defer rows.Close()

	var jobs []ReleaseJob
	for rows.Next() {
		var job ReleaseJob
		if err := rows.Scan(&job.ID, &job.Kind, &job.State, &job.EnvironmentID,
			&job.RunName, &job.RequestedBy, &job.RequestedAt, &job.FinishedAt); err != nil {
			return nil, fmt.Errorf("jobs de la release %d: %w", releaseID, err)
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// EnvironmentHistory est une entrée de l'historique d'un environnement.
type EnvironmentHistory struct {
	ReleaseTag   string
	ImageVersion string
	Tag          string
	PushedAt     time.Time
	JobID        *int64
	JobState     string
	RequestedBy  string
}

// ListEnvironmentHistory rend la suite des promotions d'un environnement.
//
// La source est ce que le dashboard a demandé, et non l'historique des commits de
// `mse-kustom` que Flux écrit. Les deux diffèrent : une promotion refusée par une
// ImagePolicy n'apparaît nulle part côté Git, alors qu'elle a bien été demandée —
// et c'est précisément ce cas qu'il faut pouvoir constater.
func (db *DB) ListEnvironmentHistory(ctx context.Context, environmentID string, limit int) ([]EnvironmentHistory, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	rows, err := db.pool.Query(ctx,
		`select r.tag, r.image_version, p.tag, p.pushed_at, p.job_id,
		        coalesce(j.state, ''), coalesce(j.requested_by, '')
		 from image_promotions p
		 join releases r on r.id = p.release_id
		 left join jobs j on j.id = p.job_id
		 where p.environment_id = $1
		 order by p.pushed_at desc
		 limit $2`, environmentID, limit)
	if err != nil {
		return nil, fmt.Errorf("historique de %q: %w", environmentID, err)
	}
	defer rows.Close()

	var history []EnvironmentHistory
	for rows.Next() {
		var entry EnvironmentHistory
		if err := rows.Scan(&entry.ReleaseTag, &entry.ImageVersion, &entry.Tag,
			&entry.PushedAt, &entry.JobID, &entry.JobState, &entry.RequestedBy); err != nil {
			return nil, fmt.Errorf("historique de %q: %w", environmentID, err)
		}
		history = append(history, entry)
	}
	return history, rows.Err()
}

// MarkReleased fait passer une release en `released` et date sa livraison.
//
// L'écriture est conditionnée à l'état de départ, comme [DB.AdvanceReleaseState] :
// deux validations simultanées, ou une validation qui arrive après un abandon, ne
// doivent pas se marcher dessus. Rend `false` quand la release a bougé entre la
// lecture et l'écriture.
//
// `released_at` est posé ici et pas ailleurs : c'est la date de la décision
// humaine, pas celle d'un déploiement. Une release validée mais jamais promue
// porte quand même sa date de validation, et c'est ce qui distingue « validée,
// pas encore livrée » de « jamais validée » dans le bon de livraison.
func (db *DB) MarkReleased(ctx context.Context, id int64, from string) (bool, error) {
	tag, err := db.pool.Exec(ctx,
		`update releases set state = $3, released_at = now(), updated_at = now()
		 where id = $1 and state = $2`, id, from, string(release.StateReleased))
	if err != nil {
		return false, fmt.Errorf("validation de la release %d: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// JobReleaseContext décrit la release qu'un job fait avancer.
type JobReleaseContext struct {
	ReleaseID int64
	// Kind est la nature du job — `build`, `promote` — et non celle de la release.
	Kind  string
	Tag   string
	State string
	// GitRef et GitCommit servent à enchaîner les vérifications sur ce qui a été
	// construit. Le commit prime : la branche a pu bouger depuis le build (B17).
	GitRef    string
	GitCommit string
}

// ErrJobWithoutRelease signale un job qui ne porte aucune release.
//
// Cas courant et non exceptionnel : un lancement manuel de tests depuis `/queue`
// n'en désigne aucune.
var ErrJobWithoutRelease = errors.New("job sans release rattachée")

// JobRelease rend la release qu'un job fait avancer, et son état courant.
//
// Lu au moment où le job se termine : l'état a pu changer entre-temps — reprise
// manuelle, annulation — et décider d'une transition sur l'état connu à
// l'enfilement produirait un écrasement silencieux.
func (db *DB) JobRelease(ctx context.Context, jobID int64) (JobReleaseContext, error) {
	var entry JobReleaseContext
	err := db.pool.QueryRow(ctx,
		`select j.kind, r.id, r.tag, r.state,
		        coalesce(r.git_ref, ''), coalesce(r.git_commit, '')
		 from jobs j join releases r on r.id = j.release_id
		 where j.id = $1`, jobID).
		Scan(&entry.Kind, &entry.ReleaseID, &entry.Tag, &entry.State,
			&entry.GitRef, &entry.GitCommit)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return JobReleaseContext{}, ErrJobWithoutRelease
		}
		return JobReleaseContext{}, fmt.Errorf("release du job %d: %w", jobID, err)
	}
	return entry, nil
}

// AdvanceReleaseState écrit un état **si** la release est encore dans celui qu'on
// a lu, et rend `false` sinon.
//
// La condition sur l'état de départ est ce qui distingue cette écriture de
// [DB.SetReleaseState] : entre la lecture et l'écriture, un autre geste a pu
// déplacer la release, et écraser son état effacerait une décision humaine par
// la conclusion d'un pipeline qui n'était plus le dernier mot.
func (db *DB) AdvanceReleaseState(ctx context.Context, id int64, from, to string) (bool, error) {
	tag, err := db.pool.Exec(ctx,
		`update releases set state = $3, updated_at = now()
		 where id = $1 and state = $2`, id, from, to)
	if err != nil {
		return false, fmt.Errorf("avancement de la release %d: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// VerificationTally compte les vérifications d'une release par issue.
type VerificationTally struct {
	Pending   int
	Failed    int
	Succeeded int
}

// Done dit si plus aucune vérification n'est en cours.
// Done dit que toutes les vérifications de la série ont rendu leur verdict.
//
// Une série **vide** n'est pas conclue. Le décompte ne portant que sur la
// dernière construction, il existe un instant — entre l'écriture de `verifying`
// et la mise en file des tests — où la série est vide. Un verdict tardif d'une
// série précédente, redélivré par l'informer à ce moment, y verrait « tout a
// conclu » et ferait passer la release en `ready` sans qu'aucun test de la série
// en cours n'ait tourné.
func (t VerificationTally) Done() bool {
	return t.Pending == 0 && t.Failed+t.Succeeded > 0
}

// CountVerifications compte les jobs de vérification d'une release.
//
// Sert à conclure `verifying` : la release n'avance que lorsque **toutes** les
// familles ont rendu leur verdict. Compter en base plutôt que retenir un état en
// mémoire est ce qui rend la conclusion correcte après un redémarrage du worker —
// les runs continuent pendant qu'il ne tourne pas.
//
// Les natures comptées sont passées par l'appelant : c'est lui qui décide de ce
// qui constitue une vérification, et cette liste vit à côté de celle qui les
// lance.
func (db *DB) CountVerifications(
	ctx context.Context, releaseID int64, kinds []string, buildKind string,
) (VerificationTally, error) {
	var tally VerificationTally
	// Le décompte ne porte que sur la **dernière** série. Une release relancée
	// après un échec traîne les vérifications de ses tentatives précédentes : les
	// compter ferait échouer indéfiniment une release dont la seconde série passe
	// entièrement, sans que rien ne le dise. La frontière est la dernière
	// construction — les vérifications d'une série sont enfilées après elle, et
	// les identifiants sont monotones.
	err := db.pool.QueryRow(ctx,
		// Le **dernier** essai de chaque famille, et non tous.
		//
		// Une famille relancée après un échec laissait ses tentatives précédentes
		// dans le décompte : trois échecs d'intégration suivis d'une réussite
		// comptaient trois échecs, et la release restait en échec pour des
		// verdicts que le dernier essai avait remplacés. Le bouton « Rejouer » ne
		// pouvait donc rien rattraper.
		`with derniers as (
		     select distinct on (kind) kind, state
		       from jobs
		      where release_id = $1
		        and kind = any($2)
		        and id > coalesce(
		            (select max(id) from jobs where release_id = $1 and kind = $3), 0)
		      order by kind, id desc
		 )
		 select
		     count(*) filter (where state in ('queued', 'running')),
		     count(*) filter (where state = 'failed'),
		     count(*) filter (where state = 'succeeded')
		 from derniers`,
		releaseID, kinds, buildKind).
		Scan(&tally.Pending, &tally.Failed, &tally.Succeeded)
	if err != nil {
		return VerificationTally{}, fmt.Errorf("vérifications de la release %d: %w", releaseID, err)
	}
	return tally, nil
}

// BuildTally compte les constructions d'une release.
type BuildTally struct {
	Pending   int
	Failed    int
	Succeeded int
}

// Done dit que toutes les images ont été construites, et aucune en échec.
//
// Une série vide n'est pas conclue, pour la même raison que les vérifications :
// entre l'écriture de `building` et la mise en file des constructions, il existe
// un instant où le décompte est nul, et une redélivrance tardive y verrait « tout
// a réussi ».
func (t BuildTally) Done() bool {
	return t.Pending == 0 && t.Failed == 0 && t.Succeeded > 0
}

// CountBuilds compte les constructions d'une release, une par image.
//
// Une release construit désormais plusieurs images — le pipeline n'en traite
// qu'une par exécution — et elle n'est construite que lorsque **toutes** ont
// abouti. Compter en base plutôt que retenir un état en mémoire est ce qui rend
// la conclusion correcte après un redémarrage du worker : les runs continuent
// pendant qu'il ne tourne pas.
//
// Le **dernier** essai de chaque image, et non tous : relancer une construction
// laisserait sinon ses tentatives précédentes dans le décompte, et une release
// dont la reprise passe entièrement resterait en échec pour un verdict que le
// dernier essai a remplacé.
//
// Le regroupement se fait sur le paramètre `image-name`, seul à dire quelle image
// un job construit. Les jobs antérieurs à ce paramètre le portent à `null` et se
// regroupent donc ensemble, ce qui est le comportement d'avant : une release
// construite par l'ancien pipeline avait bien une seule construction.
func (db *DB) CountBuilds(ctx context.Context, releaseID int64, buildKind string) (BuildTally, error) {
	var tally BuildTally
	err := db.pool.QueryRow(ctx,
		`with derniers as (
		     select distinct on (params->>'image-name') params->>'image-name' as image, state
		       from jobs
		      where release_id = $1 and kind = $2
		      order by params->>'image-name', id desc
		 )
		 select
		     count(*) filter (where state in ('queued', 'running')),
		     count(*) filter (where state = 'failed'),
		     count(*) filter (where state = 'succeeded')
		 from derniers`,
		releaseID, buildKind).
		Scan(&tally.Pending, &tally.Failed, &tally.Succeeded)
	if err != nil {
		return BuildTally{}, fmt.Errorf("constructions de la release %d: %w", releaseID, err)
	}
	return tally, nil
}

// ReleaseBadges porte ce qu'un écran affiche à côté du nom d'une release, sans
// avoir à la déplier.
type ReleaseBadges struct {
	// BuildFailed dit que la **dernière** construction a échoué.
	//
	// La dernière et non « une » : une release relancée après un échec
	// d'infrastructure garderait sinon un badge d'échec que plus rien ne justifie.
	BuildFailed bool
	// Deployed énumère les environnements qui font tourner cette release, dans
	// l'ordre de rang.
	Deployed []string
}

// ReleaseBadgesByTag rend, pour chaque release, de quoi composer ses badges.
//
// Une lecture pour toutes les releases plutôt qu'une par release : la liste en
// affiche cinquante, et cinquante allers-retours pour deux booléens et un nom
// d'environnement se paieraient à chaque ouverture de l'écran.
func (db *DB) ReleaseBadgesByTag(ctx context.Context) (map[string]ReleaseBadges, error) {
	badges := map[string]ReleaseBadges{}

	// L'état de la dernière construction de chaque release.
	builds, err := db.pool.Query(ctx,
		`select distinct on (r.tag) r.tag, j.state
		   from releases r
		   join jobs j on j.release_id = r.id and j.kind = 'build'
		  order by r.tag, j.id desc`)
	if err != nil {
		return nil, fmt.Errorf("constructions des releases: %w", err)
	}
	defer builds.Close()

	for builds.Next() {
		var tag, state string
		if err := builds.Scan(&tag, &state); err != nil {
			return nil, fmt.Errorf("constructions des releases: %w", err)
		}
		entry := badges[tag]
		entry.BuildFailed = state == JobFailed
		badges[tag] = entry
	}
	if err := builds.Err(); err != nil {
		return nil, fmt.Errorf("constructions des releases: %w", err)
	}

	// Ce que chaque environnement fait tourner, par la même règle que
	// `DeployedRelease` : la version observée désigne la release, une promotion
	// enregistrée ici tranche entre plusieurs candidates, à défaut la plus
	// récemment créée.
	//
	// C'est ce qui rend le badge exact sans condition supplémentaire : un
	// environnement n'a qu'une version observée, donc une release plus récente
	// déployée depuis fait mécaniquement disparaître le badge de la précédente.
	deployed, err := db.pool.Query(ctx,
		`select distinct on (e.id) e.id, r.tag
		   from environments e
		   join environment_observations o
		     on o.environment_id = e.id and o.portal_version is not null
		   join releases r on r.image_version = o.portal_version
		   left join image_promotions p
		     on p.release_id = r.id and p.environment_id = e.id
		  where e.visible
		  order by e.id, p.pushed_at desc nulls last, r.created_at desc`)
	if err != nil {
		return nil, fmt.Errorf("déploiements des releases: %w", err)
	}
	defer deployed.Close()

	for deployed.Next() {
		var environment, tag string
		if err := deployed.Scan(&environment, &tag); err != nil {
			return nil, fmt.Errorf("déploiements des releases: %w", err)
		}
		entry := badges[tag]
		entry.Deployed = append(entry.Deployed, environment)
		badges[tag] = entry
	}
	return badges, deployed.Err()
}
