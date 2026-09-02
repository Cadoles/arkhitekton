package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/release"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/deploykeys"
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
	// Dockerfile est le fichier qui construit cette image, quand il diffère de
	// celui du projet.
	//
	// Ajouté parce qu'un projet réel l'a exigé : `hydra-dispatcher-mse-theme`
	// porte `Dockerfile.dev` et `Dockerfile.prod` à sa racine, chacun produisant
	// une image distincte, et aucun n'a d'étapes. Le modèle ne savait dire que
	// « telle **étape** du Dockerfile du projet produit telle image », ce qui est
	// la forme du dépôt de MSE et non une vérité générale.
	//
	// Vide, celui du projet s'applique. Les deux formes coexistent donc sans que
	// l'une devienne un cas particulier de l'autre.
	Dockerfile string `json:"dockerfile,omitempty"`
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

// DockerfileFor rend le fichier qui construit cette étape.
//
// Celui de l'étape quand elle en déclare un, celui du projet sinon. L'ordre n'est
// pas arbitraire : le plus précis gagne, et un projet qui change son Dockerfile
// par défaut ne doit pas déplacer celles qui avaient déjà choisi le leur.
func (p Project) DockerfileFor(target Target) string {
	if file := strings.TrimSpace(target.Dockerfile); file != "" {
		return file
	}
	return p.Dockerfile
}

// Buildable rend les étapes qu'une release construit.
//
// Vide sans Dockerfile : un dépôt cloné par un pipeline — kustomizations,
// campagne E2E — décrit des cibles nulle part, et en inventer une lancerait une
// construction qui échouerait au premier `docker build`.
func (p Project) Buildable() []Target {
	var buildable []Target
	for _, target := range p.Targets {
		// La condition porte sur le Dockerfile **résolu** et non sur celui du
		// projet. Un dépôt peut n'en avoir aucun au niveau projet et rester
		// parfaitement constructible : `hydra-dispatcher-mse-theme` porte
		// `Dockerfile.dev` et `Dockerfile.prod` sur ses étapes, et exiger un
		// fichier de projet le déclarait non constructible.
		if strings.TrimSpace(p.DockerfileFor(target)) == "" {
			continue
		}
		buildable = append(buildable, target)
	}
	return buildable
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
	ID int64
	// Slug nomme le projet dans une URL : `/projects/mse/releases/1.8.0`. Nom
	// d'usage et non identité — celle-ci reste l'identifiant, et le slug peut
	// changer sans que rien ne se détache.
	Slug         string
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
	// Stack nomme la pile technique du projet — `php`, ou vide.
	//
	// Elle décide de la bibliothèque de référence que le dashboard pose : les
	// Task et Pipeline propres à un langage. Vide est la valeur normale d'un
	// projet qui apporte les siens (migration 0041, doc 12 §12.6).
	Stack string
	// VersionScheme nomme le schéma des tags de ce projet — `semver` ou
	// `opaque`. Vide vaut `semver`.
	//
	// Un schéma opaque reste **utilisable** : construire, tester, noter et
	// déployer par tag explicite fonctionnent. Il perd la suggestion de version
	// suivante et la sélection par plage, et l'outil doit le dire plutôt que de
	// calculer n'importe quoi (migration 0039, doc 12 §12.4).
	VersionScheme string
	// ReleaseKinds déclare les natures de release du projet. Jamais vide : la
	// migration 0040 donne à chacun le cycle historique, et un cycle vide
	// décrirait un projet déclaré et inutilisable.
	ReleaseKinds release.Cycle
	// RegistrySecret nomme le secret autorisant à pousser vers ce registre. Vide
	// vaut celui du déploiement.
	//
	// **Désigné, non créé** : un identifiant de registre vient d'un humain, et le
	// faire entrer dans un Secret depuis l'interface le ferait transiter par le
	// navigateur et l'API — laquelle tourne sans jeton de cluster. Le déploiement
	// le provisionne, le projet le nomme (migration 0038, doc 07 §7.3).
	RegistrySecret string
	MultiStage     bool
	// TrackerProject nomme ce projet dans l'outil de suivi — `mse-dev` chez
	// Redmine. Vide, le projet n'a **aucun** suivi : ses versions sont alors
	// celles que le dashboard déduit des tags, et rien n'attend de confirmation
	// extérieure.
	//
	// Porté par le projet et non par la configuration du déploiement : celle-ci
	// ne pouvait donner qu'une réponse pour tous, et deux projets suivis ensemble
	// n'ont aucune raison de partager un outil — ni même d'en avoir un.
	TrackerProject string
	// Targets porte les étapes constructibles. Une seule entrée sans `Stage`
	// décrit un Dockerfile à image unique : un second champ dirait la même chose
	// d'une autre façon, et il faudrait savoir lequel fait foi.
	Targets []Target
	// CheckedAt et CheckOK portent le constat du dernier test d'accès. Nils tant
	// qu'aucun test n'a eu lieu — état distinct d'un test qui a échoué.
	CheckedAt *time.Time
	CheckOK   *bool
	CheckNote string
	// DeployKeyActive dit que la clé a été collée dans les réglages du dépôt et
	// ouvre donc vraiment celui-ci. Faux tant que personne ne l'a affirmé : une
	// clé posée mais inconnue de la forge n'ouvre rien, et l'employer d'office
	// ferait échouer au clone un dépôt qui fonctionnait (migration 0037).
	DeployKeyActive bool
	// DeployKeyPublic est la **moitié publique** de la clé de déploiement du
	// projet. Vide tant qu'aucune clé n'a été posée.
	//
	// Elle est aussi le registre de propriété du Secret qui porte l'autre
	// moitié : la RBAC du dashboard ne lui permet pas de lire les secrets, donc
	// il ne peut pas demander au cluster si celui d'un projet lui appartient. Il
	// le lit ici (migration 0034, doc 12 §12.7).
	DeployKeyPublic    string
	DeployKeyCreatedAt *time.Time
	CreatedBy          string
	CreatedAt          time.Time
	UpdatedAt          time.Time
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
		`select id, slug, repository, label, forge_url, dockerfile, build_context,
		        image_registry, registry_secret, version_scheme, release_kinds,
		        stack, multi_stage, tracker_project, targets,
		        checked_at, check_ok,
		        check_note, deploy_key_public, deploy_key_created_at, deploy_key_active,
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
		`select id, slug, repository, label, forge_url, dockerfile, build_context,
		        image_registry, registry_secret, version_scheme, release_kinds,
		        stack, multi_stage, tracker_project, targets,
		        checked_at, check_ok,
		        check_note, deploy_key_public, deploy_key_created_at, deploy_key_active,
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

// slugPattern retient ce qu'un segment d'URL accepte.
var slugPattern = regexp.MustCompile(`[^a-z0-9-]+`)

// SlugFor dérive un nom d'URL du dépôt.
//
// Le dernier segment, ramené aux minuscules et aux caractères qu'un chemin
// accepte : `vcarroy/mse-test-project` donne `mse-test-project`. Même règle que
// la migration 0026, pour qu'un projet déclaré après coup reçoive le nom qu'il
// aurait eu s'il avait été là avant.
//
// L'unicité n'est pas garantie ici : deux dépôts homonymes chez deux
// propriétaires donnent le même slug, et c'est la contrainte en base qui tranche.
// Le dire à l'utilisateur vaut mieux que de suffixer dans son dos un nom qu'il
// devra ensuite reconnaître.
func SlugFor(repository string) string {
	last := repository
	if index := strings.LastIndex(repository, "/"); index >= 0 {
		last = repository[index+1:]
	}
	return strings.Trim(slugPattern.ReplaceAllString(strings.ToLower(last), "-"), "-")
}

// TrackedProject est un projet et le nom qu'il porte dans son outil de suivi.
type TrackedProject struct {
	ID int64
	// Slug nomme le projet côté dashboard. Rendu parce que la synchronisation
	// journalise et échoue **par projet** depuis qu'il peut y en avoir plusieurs :
	// un message qui ne dit pas lequel n'aide personne.
	Slug           string
	TrackerProject string
}

// TrackedProjects rend les projets qui déclarent un outil de suivi.
//
// Les autres n'ont rien à synchroniser : leurs versions sont celles que le
// dashboard déduit, et rien d'extérieur ne viendra les confirmer.
func (db *DB) TrackedProjects(ctx context.Context) ([]TrackedProject, error) {
	rows, err := db.pool.Query(ctx,
		`select id, slug, tracker_project from projects
		 where coalesce(trim(tracker_project), '') <> '' order by id`)
	if err != nil {
		return nil, fmt.Errorf("projets suivis: %w", err)
	}
	defer rows.Close()

	var tracked []TrackedProject
	for rows.Next() {
		var entry TrackedProject
		if err := rows.Scan(&entry.ID, &entry.Slug, &entry.TrackerProject); err != nil {
			return nil, fmt.Errorf("projets suivis: %w", err)
		}
		tracked = append(tracked, entry)
	}
	return tracked, rows.Err()
}

// ProjectBySlug rend un projet par le nom qu'il porte dans les URL.
//
// C'est la résolution que fait chaque requête portant un projet dans son chemin.
// Un slug inconnu rend [ErrProjectNotFound] et non une erreur de lecture :
// l'appelant doit répondre 404, pas 500 — une URL périmée n'est pas une panne.
func (db *DB) ProjectBySlug(ctx context.Context, slug string) (Project, error) {
	rows, err := db.pool.Query(ctx,
		`select id, slug, repository, label, forge_url, dockerfile, build_context,
		        image_registry, registry_secret, version_scheme, release_kinds,
		        stack, multi_stage, tracker_project, targets,
		        checked_at, check_ok,
		        check_note, deploy_key_public, deploy_key_created_at, deploy_key_active,
		        created_by, created_at, updated_at
		 from projects where slug = $1`, slug)
	if err != nil {
		return Project{}, fmt.Errorf("lecture du projet %q: %w", slug, err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Project{}, fmt.Errorf("lecture du projet %q: %w", slug, err)
		}
		return Project{}, ErrProjectNotFound
	}
	return scanProject(rows)
}

func scanProject(rows pgx.Rows) (Project, error) {
	var project Project
	var targets, kinds []byte
	if err := rows.Scan(&project.ID, &project.Slug, &project.Repository, &project.Label,
		&project.ForgeURL, &project.Dockerfile, &project.BuildContext,
		&project.ImageRegistry, &project.RegistrySecret, &project.VersionScheme,
		&kinds, &project.Stack, &project.MultiStage,
		&project.TrackerProject, &targets,
		&project.CheckedAt, &project.CheckOK,
		&project.CheckNote, &project.DeployKeyPublic, &project.DeployKeyCreatedAt,
		&project.DeployKeyActive,
		&project.CreatedBy, &project.CreatedAt,
		&project.UpdatedAt); err != nil {
		return Project{}, fmt.Errorf("lecture d'un projet: %w", err)
	}
	// Des étapes illisibles ne font pas disparaître le projet : le reste de sa
	// description est juste, et un projet absent de l'écran se chercherait dans
	// la forge avant de se chercher en base.
	if len(targets) > 0 {
		_ = json.Unmarshal(targets, &project.Targets)
	}
	// Un cycle illisible retombe sur celui d'origine plutôt que de laisser le
	// projet sans nature créable : la colonne est contrainte non vide, et un
	// décodage qui échoue désigne un défaut du dashboard, pas de la déclaration.
	if len(kinds) > 0 {
		_ = json.Unmarshal(kinds, &project.ReleaseKinds)
	}
	if len(project.ReleaseKinds) == 0 {
		project.ReleaseKinds = release.DefaultCycle()
	}
	return project, nil
}

// CreateProject déclare un dépôt.
func (db *DB) CreateProject(ctx context.Context, project Project) (int64, error) {
	targets, err := json.Marshal(normaliseTargets(project.Targets))
	if err != nil {
		return 0, fmt.Errorf("sérialisation des étapes: %w", err)
	}

	if strings.TrimSpace(project.Slug) == "" {
		project.Slug = SlugFor(project.Repository)
	}

	// Le cycle est sérialisé ici plutôt que laissé à l'appelant : la colonne est
	// contrainte non vide, et un projet déclaré sans nature serait refusé par la
	// base avec un message qui ne désigne rien d'utile.
	cycle := project.ReleaseKinds
	if len(cycle) == 0 {
		cycle = release.DefaultCycle()
	}
	kinds, err := json.Marshal(cycle)
	if err != nil {
		return 0, fmt.Errorf("sérialisation du cycle: %w", err)
	}

	var id int64
	err = db.pool.QueryRow(ctx,
		`insert into projects (slug, repository, label, forge_url, dockerfile,
		                       build_context, image_registry, registry_secret,
		                       version_scheme, release_kinds, stack, multi_stage,
		                       tracker_project, targets, created_by)
		 values ($1, $2, $3, $4, $5, $6, $7, $8, coalesce(nullif($9, ''), 'semver'),
		         $10, $11, $12, $13, $14, $15)
		 on conflict (repository) do nothing
		 returning id`,
		project.Slug, project.Repository, project.Label, project.ForgeURL, project.Dockerfile,
		project.BuildContext, project.ImageRegistry, project.RegistrySecret,
		project.VersionScheme, kinds, project.Stack, project.MultiStage,
		project.TrackerProject, targets, project.CreatedBy).Scan(&id)
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

	if strings.TrimSpace(project.Slug) == "" {
		project.Slug = SlugFor(project.Repository)
	}

	// Le cycle est sérialisé ici plutôt que laissé à l'appelant : la colonne est
	// contrainte non vide, et un projet déclaré sans nature serait refusé par la
	// base avec un message qui ne désigne rien d'utile.
	cycle := project.ReleaseKinds
	if len(cycle) == 0 {
		cycle = release.DefaultCycle()
	}
	kinds, err := json.Marshal(cycle)
	if err != nil {
		return fmt.Errorf("sérialisation du cycle: %w", err)
	}

	tag, err := db.pool.Exec(ctx,
		`update projects
		 set repository = $2, label = $3, forge_url = $4, dockerfile = $5,
		     build_context = $6, image_registry = $7, multi_stage = $8,
		     targets = $9, slug = $10, tracker_project = $11, registry_secret = $12,
		     version_scheme = coalesce(nullif($13, ''), 'semver'),
		     release_kinds = $14, stack = $15,
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
		project.MultiStage, targets, project.Slug, project.TrackerProject,
		project.RegistrySecret, project.VersionScheme, kinds, project.Stack)
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
		// L'image seule suffit : une étape peut n'avoir ni cible ni Dockerfile
		// propre — c'est le cas d'un dépôt à Dockerfile unique — mais sans nom
		// d'image elle ne désigne rien à publier.
		if strings.TrimSpace(target.Image) == "" {
			continue
		}
		kept = append(kept, target)
	}
	return kept
}

// SetDeployKey enregistre la moitié publique de la clé d'un projet.
//
// Écrite **après** que le Secret a été déposé dans le cluster, jamais avant :
// cette colonne est le registre de propriété, et l'inscrire d'abord ferait
// croire à une clé posée si le dépôt échouait ensuite. Une rotation ultérieure
// remplacerait alors un Secret qui n'existe pas, ou pire, celui d'un tiers.
func (db *DB) SetDeployKey(ctx context.Context, projectID int64, public string) error {
	_, err := db.pool.Exec(ctx,
		// La demande de rotation est effacée dans le même geste : la laisser
		// ferait reposer une clé à chaque tour de la boucle.
		`update projects
		 set deploy_key_public = $2, deploy_key_created_at = now(),
		     deploy_key_rotate_requested_at = null, updated_at = now()
		 where id = $1`, projectID, public)
	if err != nil {
		return fmt.Errorf("clé du projet %d: %w", projectID, err)
	}
	return nil
}

// ClearDeployKey oublie la clé d'un projet.
//
// Rend le projet à l'état « aucune clé » : une pose ultérieure refusera d'adopter
// un Secret homonyme, ce qui est le comportement voulu — si l'on a oublié la clé,
// on ne peut plus affirmer que le Secret est le nôtre.
func (db *DB) ClearDeployKey(ctx context.Context, projectID int64) error {
	_, err := db.pool.Exec(ctx,
		`update projects
		 set deploy_key_public = '', deploy_key_created_at = null, updated_at = now()
		 where id = $1`, projectID)
	if err != nil {
		return fmt.Errorf("oubli de la clé du projet %d: %w", projectID, err)
	}
	return nil
}

// DeployKeySecret rend le nom du secret portant la clé d'un projet.
//
// Faux quand le projet n'a pas de clé propre. La réponse se lit dans
// `deploy_key_public` et non dans le cluster : la RBAC du dashboard ne lui
// permet pas d'y lire un secret, et c'est cette colonne qui fait foi sur la
// propriété (migration 0034).
func (db *DB) DeployKeySecret(ctx context.Context, projectID int64) (string, bool, error) {
	var slug, public string
	err := db.pool.QueryRow(ctx,
		// `deploy_key_active` et non la seule présence de la clé : entre la pose
		// et l'instant où quelqu'un la colle dans la forge, elle n'ouvre rien.
		// L'employer d'office ferait échouer au clone un dépôt qui fonctionnait
		// la veille (migration 0037).
		`select slug, deploy_key_public from projects
		 where id = $1 and deploy_key_active`, projectID).
		Scan(&slug, &public)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("clé du projet %d: %w", projectID, err)
	case public == "":
		return "", false, nil
	}
	return deploykeys.SecretName(slug), true, nil
}

// RequestKeyRotation demande le remplacement de la clé d'un projet.
//
// Une demande et non un remplacement : l'API tourne sans jeton de cluster, et
// lui en donner un pour la commodité d'une pose synchrone serait un recul de
// sécurité — c'est la surface exposée. Le worker, qui a déjà le cluster et le
// bail de leader, la réalise (migration 0034).
func (db *DB) RequestKeyRotation(ctx context.Context, projectID int64) error {
	tag, err := db.pool.Exec(ctx,
		`update projects set deploy_key_rotate_requested_at = now(), updated_at = now()
		 where id = $1`, projectID)
	if err != nil {
		return fmt.Errorf("demande de rotation pour le projet %d: %w", projectID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// PendingKey décrit un projet dont la clé est à poser.
type PendingKey struct {
	ID   int64
	Slug string
	// Known dit si le dashboard possède déjà la clé de ce projet. C'est ce qui
	// distingue une rotation — le Secret est nôtre, on le remplace — d'une
	// première pose, où un Secret homonyme viendrait forcément d'ailleurs et ne
	// doit pas être adopté.
	Known bool
}

// ProjectsAwaitingKey rend les projets qu'il reste à pourvoir.
//
// Deux cas réunis, parce que la boucle les traite de la même façon : celui qui
// n'a aucune clé, et celui dont une rotation a été demandée. Un redémarrage
// après panne rattrape les deux sans qu'on ait à distinguer ce qui avait été
// interrompu.
func (db *DB) ProjectsAwaitingKey(ctx context.Context) ([]PendingKey, error) {
	rows, err := db.pool.Query(ctx,
		`select id, slug, deploy_key_public <> ''
		 from projects
		 where deploy_key_public = '' or deploy_key_rotate_requested_at is not null
		 order by id`)
	if err != nil {
		return nil, fmt.Errorf("projets sans clé: %w", err)
	}
	defer rows.Close()

	var pending []PendingKey
	for rows.Next() {
		var entry PendingKey
		if err := rows.Scan(&entry.ID, &entry.Slug, &entry.Known); err != nil {
			return nil, fmt.Errorf("projets sans clé: %w", err)
		}
		pending = append(pending, entry)
	}
	return pending, rows.Err()
}

// ActivateDeployKey déclare que la clé d'un projet ouvre son dépôt.
//
// Le geste que rien n'automatise : coller la moitié publique dans les réglages
// du dépôt appartient à un humain, et lui seul peut dire que c'est fait. Tant
// qu'il ne l'a pas dit, les pipelines montent la clé partagée — ce qui marche —
// plutôt qu'une clé neuve que la forge ignore.
func (db *DB) ActivateDeployKey(ctx context.Context, projectID int64, active bool) error {
	tag, err := db.pool.Exec(ctx,
		`update projects set deploy_key_active = $2, updated_at = now() where id = $1`,
		projectID, active)
	if err != nil {
		return fmt.Errorf("activation de la clé du projet %d: %w", projectID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// RegistrySecret rend le secret de registre désigné par un projet.
//
// Faux quand il n'en désigne aucun : le run emploie alors celui du déploiement,
// qui est le cas de tous les projets qui publient au même endroit.
func (db *DB) RegistrySecret(ctx context.Context, projectID int64) (string, bool, error) {
	var name string
	err := db.pool.QueryRow(ctx,
		`select registry_secret from projects where id = $1`, projectID).Scan(&name)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("secret de registre du projet %d: %w", projectID, err)
	case name == "":
		return "", false, nil
	}
	return name, true, nil
}

// Stacks rend les piles déclarées par les projets.
//
// Dédoublonnées, et le vide écarté : c'est la liste des bibliothèques que le
// dashboard a effectivement à poser, et itérer sur les projets en reposerait
// plusieurs fois la même.
func (db *DB) Stacks(ctx context.Context) ([]string, error) {
	rows, err := db.pool.Query(ctx,
		`select distinct stack from projects where stack <> '' order by stack`)
	if err != nil {
		return nil, fmt.Errorf("piles déclarées: %w", err)
	}
	defer rows.Close()

	var stacks []string
	for rows.Next() {
		var stack string
		if err := rows.Scan(&stack); err != nil {
			return nil, fmt.Errorf("piles déclarées: %w", err)
		}
		stacks = append(stacks, stack)
	}
	return stacks, rows.Err()
}
