package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

/*
 * Les dépôts que le dashboard pilote.
 *
 * Descriptif pour l'instant : la table dit ce que le dashboard croit piloter et
 * ce qu'il sait en construire, mais les pipelines reçoivent encore leurs dépôts
 * par configuration. Le raccordement demandera de trancher ce que l'ordonnanceur
 * accepte d'une demande, et cette décision mérite d'être prise seule.
 */

// Target est une étape constructible et le dépôt d'image qu'elle alimente.
type Target struct {
	// Stage est la cible du Dockerfile — `web`, `cli`, `dev`.
	Stage string `json:"stage"`
	// Image est le dépôt publié, sans le projet de registre — `portal-cli`.
	Image string `json:"image"`
}

// Project décrit un dépôt piloté.
type Project struct {
	ID           int64
	Repository   string
	Label        string
	ForgeURL     string
	Dockerfile   string
	BuildContext string
	MultiStage   bool
	Targets      []Target
	// CheckedAt et CheckOK portent le constat du dernier test d'accès. Nils tant
	// qu'aucun test n'a eu lieu — état distinct d'un test qui a échoué.
	CheckedAt *time.Time
	CheckOK   *bool
	CheckNote string
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ErrProjectNotFound signale un projet absent.
var ErrProjectNotFound = errors.New("projet introuvable")

// ErrProjectExists signale un dépôt déjà déclaré.
//
// Distinct d'une erreur d'écriture : deux entrées pour un même dépôt décriraient
// la même chose deux fois, et rien ne dirait laquelle fait foi. L'appelant doit
// pouvoir le dire à l'utilisateur plutôt que de rendre une panne.
var ErrProjectExists = errors.New("dépôt déjà déclaré")

// ListProjects rend les projets déclarés, par libellé.
func (db *DB) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := db.pool.Query(ctx,
		`select id, repository, label, forge_url, dockerfile, build_context,
		        multi_stage, targets, checked_at, check_ok, check_note,
		        created_by, created_at, updated_at
		 from projects order by label`)
	if err != nil {
		return nil, fmt.Errorf("liste des projets: %w", err)
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

// GetProject rend un projet.
func (db *DB) GetProject(ctx context.Context, id int64) (Project, error) {
	rows, err := db.pool.Query(ctx,
		`select id, repository, label, forge_url, dockerfile, build_context,
		        multi_stage, targets, checked_at, check_ok, check_note,
		        created_by, created_at, updated_at
		 from projects where id = $1`, id)
	if err != nil {
		return Project{}, fmt.Errorf("lecture du projet %d: %w", id, err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Project{}, fmt.Errorf("lecture du projet %d: %w", id, err)
		}
		return Project{}, ErrProjectNotFound
	}
	return scanProject(rows)
}

func scanProject(rows pgx.Rows) (Project, error) {
	var project Project
	var targets []byte
	if err := rows.Scan(&project.ID, &project.Repository, &project.Label,
		&project.ForgeURL, &project.Dockerfile, &project.BuildContext,
		&project.MultiStage, &targets, &project.CheckedAt, &project.CheckOK,
		&project.CheckNote, &project.CreatedBy, &project.CreatedAt,
		&project.UpdatedAt); err != nil {
		return Project{}, fmt.Errorf("lecture d'un projet: %w", err)
	}
	// Des étapes illisibles ne font pas disparaître le projet : le reste de sa
	// description est juste, et un projet absent de l'écran se chercherait dans
	// la forge avant de se chercher en base.
	if len(targets) > 0 {
		_ = json.Unmarshal(targets, &project.Targets)
	}
	return project, nil
}

// CreateProject déclare un dépôt.
func (db *DB) CreateProject(ctx context.Context, project Project) (int64, error) {
	targets, err := json.Marshal(normaliseTargets(project.Targets))
	if err != nil {
		return 0, fmt.Errorf("sérialisation des étapes: %w", err)
	}

	var id int64
	err = db.pool.QueryRow(ctx,
		`insert into projects (repository, label, forge_url, dockerfile,
		                       build_context, multi_stage, targets, created_by)
		 values ($1, $2, $3, $4, $5, $6, $7, $8)
		 on conflict (repository) do nothing
		 returning id`,
		project.Repository, project.Label, project.ForgeURL, project.Dockerfile,
		project.BuildContext, project.MultiStage, targets, project.CreatedBy).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// `do nothing` n'a rien rendu : le dépôt est déjà déclaré.
			return 0, ErrProjectExists
		}
		return 0, fmt.Errorf("déclaration du projet %q: %w", project.Repository, err)
	}
	return id, nil
}

// UpdateProject remplace la description d'un projet.
//
// Le dépôt n'en fait pas partie : le changer ferait d'un projet un autre, et les
// constats d'accès qui l'accompagnent porteraient alors sur ce qui n'a pas été
// testé. Renommer un dépôt se fait en le redéclarant.
func (db *DB) UpdateProject(ctx context.Context, project Project) error {
	targets, err := json.Marshal(normaliseTargets(project.Targets))
	if err != nil {
		return fmt.Errorf("sérialisation des étapes: %w", err)
	}

	tag, err := db.pool.Exec(ctx,
		`update projects
		 set label = $2, forge_url = $3, dockerfile = $4, build_context = $5,
		     multi_stage = $6, targets = $7, updated_at = now()
		 where id = $1`,
		project.ID, project.Label, project.ForgeURL, project.Dockerfile,
		project.BuildContext, project.MultiStage, targets)
	if err != nil {
		return fmt.Errorf("mise à jour du projet %d: %w", project.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// DeleteProject retire un dépôt de la liste.
func (db *DB) DeleteProject(ctx context.Context, id int64) error {
	tag, err := db.pool.Exec(ctx, `delete from projects where id = $1`, id)
	if err != nil {
		return fmt.Errorf("suppression du projet %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// RecordProjectCheck consigne le résultat d'un test d'accès.
//
// Conservé plutôt que recalculé à l'affichage : l'appel sort du dashboard, et le
// refaire à chaque ouverture de l'écran ferait payer une latence de forge pour
// une information qui change une fois par trimestre.
func (db *DB) RecordProjectCheck(ctx context.Context, id int64, ok bool, note string) error {
	tag, err := db.pool.Exec(ctx,
		`update projects
		 set checked_at = now(), check_ok = $2, check_note = $3, updated_at = now()
		 where id = $1`, id, ok, note)
	if err != nil {
		return fmt.Errorf("constat d'accès du projet %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// normaliseTargets écarte les étapes vides.
//
// Un formulaire laisse volontiers une ligne à moitié remplie derrière lui.
// L'enregistrer donnerait une étape sans nom, qu'aucun build ne saurait
// construire et que rien à l'écran ne distinguerait d'une étape valide.
func normaliseTargets(targets []Target) []Target {
	kept := make([]Target, 0, len(targets))
	for _, target := range targets {
		if target.Stage == "" && target.Image == "" {
			continue
		}
		kept = append(kept, target)
	}
	return kept
}
