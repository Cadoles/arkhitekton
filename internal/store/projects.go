package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	// Stage est la cible du Dockerfile — `web`, `cli`, `dev`. Vide sur un
	// Dockerfile à image unique, qui n'en a pas.
	Stage string `json:"stage"`
	// Image est le nom court du dépôt publié — `portal-cli`. Le registre qui le
	// préfixe vit sur le projet : il est le même pour toutes les étapes.
	Image string `json:"image"`
	// Promote dit si un re-tag propage cette image. Faux pour une image que la
	// construction publie sans qu'aucun environnement du flux ne la déploie —
	// l'image de développement : la re-taguer en préproduction reviendrait à
	// annoncer comme livrable quelque chose qui ne l'est pas.
	//
	// Pointeur, pour distinguer « non promue » d'une entrée écrite avant que le
	// champ existe. Une entrée muette est traitée comme promue : c'est le cas
	// courant, et le taire est ce que faisait le code qui promouvait tout.
	Promote *bool `json:"promote,omitempty"`
}

// Promoted dit si un re-tag propage cette image.
func (t Target) Promoted() bool { return t.Promote == nil || *t.Promote }

// Reference compose la référence complète d'une image, sans tag.
//
// Vide quand l'un des deux manque : un nom sans registre ressemble à une
// référence tirable sans l'être, et c'est le genre de chaîne qu'on recopie dans
// un déploiement avant de découvrir qu'elle ne désigne rien.
func (p Project) Reference(target Target) string {
	registry := strings.Trim(strings.TrimSpace(p.ImageRegistry), "/")
	image := strings.TrimSpace(target.Image)
	if registry == "" || image == "" {
		return ""
	}
	return registry + "/" + image
}

// Buildable rend les étapes qu'une release construit.
//
// Vide sans Dockerfile : un dépôt cloné par un pipeline — kustomizations,
// campagne E2E — décrit des cibles nulle part, et en inventer une lancerait une
// construction qui échouerait au premier `docker build`.
func (p Project) Buildable() []Target {
	if strings.TrimSpace(p.Dockerfile) == "" {
		return nil
	}
	return p.Targets
}

// Promotable rend les étapes qu'un re-tag propage.
func (p Project) Promotable() []Target {
	var promoted []Target
	for _, target := range p.Buildable() {
		if target.Promoted() {
			promoted = append(promoted, target)
		}
	}
	return promoted
}

// Project décrit un dépôt piloté.
type Project struct {
	ID           int64
	Repository   string
	Label        string
	ForgeURL     string
	Dockerfile   string
	BuildContext string
	// ImageRegistry est le registre et le projet où publier, sans le nom
	// d'image : `reg.cadoles.com/cnous-mse`. Porté par le projet et non répété
	// par étape — les images d'une application vont au même endroit, et les
	// répéter ferait diverger la dernière au premier changement de registre.
	ImageRegistry string
	MultiStage    bool
	// Targets porte les étapes constructibles. Une seule entrée sans `Stage`
	// décrit un Dockerfile à image unique : un second champ dirait la même chose
	// d'une autre façon, et il faudrait savoir lequel fait foi.
	Targets []Target
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

// ErrNoBuildableProject signale qu'aucun dépôt déclaré n'est constructible.
//
// Distinct d'une erreur de lecture : la table est joignable, elle ne décrit
// simplement rien à construire. Une release ne peut alors pas démarrer, et le
// dire ainsi vaut mieux que d'enfiler zéro construction — le décompte
// conclurait aussitôt sur une release sans image.
var ErrNoBuildableProject = errors.New("aucun dépôt constructible déclaré")

// BuildableProject rend le dépôt dont une release construit les images.
//
// Un seul, pour l'instant : le dashboard versionne une application, et l'appelant
// n'a nulle part où recevoir un choix. Plusieurs candidats sont refusés plutôt
// que départagés par l'ordre de la table — en construire un au hasard produirait
// une release dont personne ne pourrait dire ce qu'elle contient.
//
// C'est le point où la table remplace les constantes qui portaient `portal` et
// `portal-cli` en Go (doc 12 §12.9).
func (db *DB) BuildableProject(ctx context.Context) (Project, error) {
	projects, err := db.ListProjects(ctx)
	if err != nil {
		return Project{}, err
	}

	var found []Project
	for _, project := range projects {
		if len(project.Buildable()) > 0 {
			found = append(found, project)
		}
	}

	switch len(found) {
	case 0:
		return Project{}, ErrNoBuildableProject
	case 1:
		return found[0], nil
	default:
		names := make([]string, 0, len(found))
		for _, project := range found {
			names = append(names, project.Repository)
		}
		return Project{}, fmt.Errorf(
			"%w: plusieurs candidats (%s)", ErrProjectAmbiguous, strings.Join(names, ", "))
	}
}

// ErrProjectAmbiguous signale plusieurs dépôts constructibles.
var ErrProjectAmbiguous = errors.New("dépôt constructible ambigu")

// uniqueViolation est le code PostgreSQL d'une contrainte d'unicité rompue.
const uniqueViolation = "23505"

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
		        image_registry, multi_stage, targets, checked_at, check_ok,
		        check_note, created_by, created_at, updated_at
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
		        image_registry, multi_stage, targets, checked_at, check_ok,
		        check_note, created_by, created_at, updated_at
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
		&project.ImageRegistry, &project.MultiStage, &targets,
		&project.CheckedAt, &project.CheckOK,
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
		                       build_context, image_registry, multi_stage,
		                       targets, created_by)
		 values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 on conflict (repository) do nothing
		 returning id`,
		project.Repository, project.Label, project.ForgeURL, project.Dockerfile,
		project.BuildContext, project.ImageRegistry, project.MultiStage,
		targets, project.CreatedBy).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// `do nothing` n'a rien rendu : le dépôt est déjà déclaré.
			return 0, ErrProjectExists
		}
		return 0, fmt.Errorf("déclaration du projet %q: %w", project.Repository, err)
	}
	return id, nil
}

// UpdateProject remplace la description d'un projet, dépôt compris.
//
// Le dépôt a d'abord été figé, au motif qu'en changer ferait d'un projet un
// autre. C'était vrai et sans importance : les projets amorcés portent le dépôt
// que la **configuration** a nommé, et corriger celui-là est exactement ce qu'on
// vient faire — le figer obligeait à supprimer puis redéclarer, en perdant tout
// ce qui accompagnait la ligne.
//
// Ce qui était juste dans ce motif est conservé : changer de dépôt **efface le
// constat d'accès**. Il portait sur ce qu'on ne pilote plus, et le garder
// afficherait « accès vérifié » sur un dépôt que personne n'a testé — la pire
// des trois réponses possibles.
func (db *DB) UpdateProject(ctx context.Context, project Project) error {
	// Un dépôt vide n'est jamais une intention : c'est un appelant qui a oublié
	// le champ. Le laisser passer l'effacerait en silence, et le projet
	// deviendrait une ligne qui ne désigne rien — ce qu'aucun écran ne saurait
	// expliquer. Constaté sur un test écrit du temps où le dépôt était figé.
	if strings.TrimSpace(project.Repository) == "" {
		return fmt.Errorf("mise à jour du projet %d: dépôt vide", project.ID)
	}

	targets, err := json.Marshal(normaliseTargets(project.Targets))
	if err != nil {
		return fmt.Errorf("sérialisation des étapes: %w", err)
	}

	tag, err := db.pool.Exec(ctx,
		`update projects
		 set repository = $2, label = $3, forge_url = $4, dockerfile = $5,
		     build_context = $6, image_registry = $7, multi_stage = $8,
		     targets = $9,
		     -- Le constat ne survit qu'à un dépôt inchangé. La comparaison porte
		     -- sur la valeur en base, encore lisible pendant l'update : une
		     -- lecture préalable ouvrirait une fenêtre entre le constat et
		     -- l'écriture.
		     checked_at = case when repository = $2 then checked_at end,
		     check_ok   = case when repository = $2 then check_ok end,
		     check_note = case when repository = $2 then check_note else '' end,
		     updated_at = now()
		 where id = $1`,
		project.ID, project.Repository, project.Label, project.ForgeURL,
		project.Dockerfile, project.BuildContext, project.ImageRegistry,
		project.MultiStage, targets)
	if err != nil {
		// Un dépôt déjà déclaré ailleurs : deux entrées le décriraient, et rien
		// ne dirait laquelle fait foi. L'appelant doit pouvoir le dire plutôt que
		// de rendre une panne.
		//
		// Le code d'erreur plutôt que le message : celui-ci est traduit selon la
		// locale du serveur, et le comparer marcherait ici pour cesser de marcher
		// sur une base configurée autrement.
		var violation *pgconn.PgError
		if errors.As(err, &violation) && violation.Code == uniqueViolation {
			return ErrProjectExists
		}
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
