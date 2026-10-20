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

	"forge.cadoles.com/vcarroy/arkhitekton/internal/release"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/deploykeys"
	"github.com/jackc/pgx/v5/pgconn"
)

/*
 * The repositories the dashboard drives.
 *
 * Descriptive for now: the table says what the dashboard believes it drives and what it can build
 * of it, but the pipelines still receive their repositories by configuration. Wiring them up will
 * require settling what the scheduler accepts from a request, and that decision deserves to be
 * taken on its own.
 */

// Target is a buildable stage and the image repository it feeds.
type Target struct {
	// Stage is the Dockerfile target — `web`, `cli`, `dev`. Empty on a single-image
	// Dockerfile, which has none.
	Stage string `json:"stage"`
	// Image is the short name of the published repository. The registry that prefixes
	// it lives on the project: it is the same for every stage.
	Image string `json:"image"`
	// Dockerfile is the file that builds this image, when it differs from the
	// project's.
	//
	// Added because a real project required it: one repository carries
	// `Dockerfile.dev` and `Dockerfile.prod` at its root, each producing a distinct
	// image, and neither has stages. The model could only say "this stage of the
	// project's Dockerfile produces that image", which is one repository's shape and
	// not a general truth.
	//
	// Empty, the project's applies. Both forms therefore coexist without either
	// becoming a special case of the other.
	Dockerfile string `json:"dockerfile,omitempty"`
	// Promote says whether a re-tag propagates this image. False for an image the
	// build publishes that no environment of the flow deploys — a development image:
	// re-tagging it to pre-production would announce as shippable something that is
	// not.
	//
	// A pointer, to tell "not promoted" from an entry written before the field
	// existed. A silent entry is treated as promoted: that is the common case, and
	// staying silent is what the code that promoted everything did.
	Promote *bool `json:"promote,omitempty"`
}

// Promoted says whether a re-tag propagates this image.
func (t Target) Promoted() bool { return t.Promote == nil || *t.Promote }

// Reference composes an image's full reference, without a tag.
//
// Empty when either half is missing: a name without a registry looks like a
// pullable reference without being one, and that is the kind of string someone
// copies into a deployment before finding it names nothing.
func (p Project) Reference(target Target) string {
	registry := strings.Trim(strings.TrimSpace(p.ImageRegistry), "/")
	image := strings.TrimSpace(target.Image)
	if registry == "" || image == "" {
		return ""
	}
	return registry + "/" + image
}

// DockerfileFor returns the file that builds this stage.
//
// The stage's when it declares one, the project's otherwise. The order is not
// arbitrary: the most specific wins, and a project changing its default Dockerfile
// must not move those that had already chosen their own.
func (p Project) DockerfileFor(target Target) string {
	if file := strings.TrimSpace(target.Dockerfile); file != "" {
		return file
	}
	return p.Dockerfile
}

// Buildable returns the stages a release builds.
//
// Empty with no Dockerfile: a repository cloned by a pipeline — kustomizations, an
// end-to-end campaign — describes targets nowhere, and inventing one would launch a
// build failing at the first `docker build`.
func (p Project) Buildable() []Target {
	var buildable []Target
	for _, target := range p.Targets {
		// The condition is on the resolved Dockerfile and not on the project's. A
		// repository may have none at project level and still be perfectly buildable when
		// its stages carry their own, and requiring a project file declared it
		// unbuildable.
		if strings.TrimSpace(p.DockerfileFor(target)) == "" {
			continue
		}
		buildable = append(buildable, target)
	}
	return buildable
}

// Promotable returns the stages a re-tag propagates.
func (p Project) Promotable() []Target {
	var promoted []Target
	for _, target := range p.Buildable() {
		if target.Promoted() {
			promoted = append(promoted, target)
		}
	}
	return promoted
}

// Project describes a driven repository.
type Project struct {
	ID int64
	// Slug names the project in a URL: `/projects/mse/releases/1.8.0`. A usage name and
	// not an identity — that stays the identifier, and the slug can change without
	// anything coming loose.
	Slug         string
	Repository   string
	Label        string
	ForgeURL     string
	Dockerfile   string
	BuildContext string
	// ImageRegistry is the registry and project to publish to, without the image name.
	// Carried by the project and not repeated per stage — an application's images go to
	// the same place, and repeating it would let the last one diverge at the first
	// registry change.
	ImageRegistry string
	// Stack names the project's technology stack — `php`, or empty.
	//
	// It decides which reference library the dashboard installs: the Tasks and
	// Pipelines specific to a language. Empty is the normal value for a project that
	// brings its own.
	Stack string
	// VersionScheme names this project's tag scheme — `semver` or `opaque`. Empty means
	// `semver`.
	//
	// An opaque scheme stays usable: building, testing, grading and deploying by
	// explicit tag all work. It loses the next-version suggestion and range selection,
	// and the tool must say so rather than compute something arbitrary.
	VersionScheme string
	// ReleaseKinds declares the project's release kinds. Never empty: the migration
	// gives each project the historical cycle, and an empty cycle would describe a
	// project that is declared and unusable.
	ReleaseKinds release.Cycle
	// RegistrySecret names the secret allowing pushes to this registry. Empty means the
	// deployment's.
	//
	// Named, not created: a registry credential comes from a human, and putting it into
	// a Secret from the interface would route it through the browser and the API —
	// which runs without a cluster token. The deployment provisions it, the project
	// names it. See docs/09-security.md.
	RegistrySecret string
	MultiStage     bool
	// TrackerProject names this project in the tracking tool. Empty means the project
	// has no tracker at all: its versions are then those the dashboard derives from
	// tags, and nothing waits for outside confirmation.
	//
	// Carried by the project and not by the deployment's configuration: that could only
	// give one answer for all, and two projects tracked together have no reason to
	// share a tool — or to have one at all.
	TrackerProject string
	// Targets carries the buildable stages. A single entry with no Stage describes a
	// single-image Dockerfile: a second field would say the same thing another way, and
	// one would have to know which prevails.
	Targets []Target
	// CheckedAt and CheckOK carry the verdict of the last access check. Nil while no
	// check has happened — a state distinct from a check that failed.
	CheckedAt *time.Time
	CheckOK   *bool
	CheckNote string
	// DeployKeyActive says the key has been pasted into the repository's settings and
	// therefore really opens it. False while nobody has asserted it: a key that is set
	// but unknown to the forge opens nothing, and using it regardless would fail the
	// clone of a repository that was working.
	DeployKeyActive bool
	// DeployKeyPublic is the public half of the project's deploy key. Empty while no key
	// has been set.
	//
	// It is also the ownership record for the Secret carrying the other half: the
	// dashboard's RBAC does not let it read secrets, so it cannot ask the cluster
	// whether a project's secret belongs to it. It reads it here.
	DeployKeyPublic    string
	DeployKeyCreatedAt *time.Time
	CreatedBy          string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// ErrProjectNotFound reports a missing project.
var ErrProjectNotFound = errors.New("project not found")

// ErrNoBuildableProject reports that no declared repository is buildable.
//
// Distinct from a read error: the table is reachable, it simply does not describe
var ErrNoBuildableProject = errors.New("no buildable repository declared")

// BuildableProject returns the repository whose images a release builds.
//
// One only, for now: the dashboard versions one application, and the caller has
// nowhere to receive a choice. Several candidates are refused rather than settled by
// table order — building one at random would produce a release nobody could
// describe.
//
// This is the point where the table replaces the image names that used to be Go
// constants.
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

// ErrProjectAmbiguous reports several buildable repositories.
var ErrProjectAmbiguous = errors.New("ambiguous buildable repository")

// uniqueViolation is PostgreSQL's code for a broken uniqueness constraint.
const uniqueViolation = "23505"

// ErrProjectExists reports a repository already declared.
//
// Distinct from a write error: two entries for one repository would describe the
// same thing twice, with nothing saying which prevails. The caller must be able to
// tell the user rather than return a fault.
var ErrProjectExists = errors.New("repository already declared")

// ListProjects returns the declared projects, by label.
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
		return nil, fmt.Errorf("listing the projects: %w", err)
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

// GetProject returns a project.
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
		return Project{}, fmt.Errorf("reading project %d: %w", id, err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Project{}, fmt.Errorf("reading project %d: %w", id, err)
		}
		return Project{}, ErrProjectNotFound
	}
	return scanProject(rows)
}

// slugPattern keeps what a URL segment accepts.
var slugPattern = regexp.MustCompile(`[^a-z0-9-]+`)

// SlugFor derives a URL name from the repository.
//
// The last segment, lowercased and reduced to path-safe characters. The same rule as
// the migration, so a project declared later receives the name it would have had had
// it been there before.
//
// Uniqueness is not guaranteed here: two repositories of the same name under two
// owners give the same slug, and it is the database constraint that settles it.
// Telling the user beats silently suffixing a name they will then have to recognise.
func SlugFor(repository string) string {
	last := repository
	if index := strings.LastIndex(repository, "/"); index >= 0 {
		last = repository[index+1:]
	}
	return strings.Trim(slugPattern.ReplaceAllString(strings.ToLower(last), "-"), "-")
}

// TrackedProject is a project and the name it carries in its tracking tool.
type TrackedProject struct {
	ID int64
	// Slug names the project on the dashboard's side. Returned because synchronisation
	// logs and fails per project now that there may be several: a message that does not
	// say which helps nobody.
	Slug           string
	TrackerProject string
}

// TrackedProjects returns the projects that declare a tracking tool.
//
// The others have nothing to synchronise: their versions are the ones the dashboard
// derives, and nothing external will confirm them.
func (db *DB) TrackedProjects(ctx context.Context) ([]TrackedProject, error) {
	rows, err := db.pool.Query(ctx,
		`select id, slug, tracker_project from projects
		 where coalesce(trim(tracker_project), '') <> '' order by id`)
	if err != nil {
		return nil, fmt.Errorf("tracked projects: %w", err)
	}
	defer rows.Close()

	var tracked []TrackedProject
	for rows.Next() {
		var entry TrackedProject
		if err := rows.Scan(&entry.ID, &entry.Slug, &entry.TrackerProject); err != nil {
			return nil, fmt.Errorf("tracked projects: %w", err)
		}
		tracked = append(tracked, entry)
	}
	return tracked, rows.Err()
}

// ProjectBySlug returns a project by the name it carries in URLs.
//
// This is the resolution every request with a project in its path performs. An
// unknown slug returns [ErrProjectNotFound] and not a read error: the caller must
// answer 404, not 500 — a stale URL is not a fault.
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
		return Project{}, fmt.Errorf("reading project %q: %w", slug, err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Project{}, fmt.Errorf("reading project %q: %w", slug, err)
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
		return Project{}, fmt.Errorf("reading a project: %w", err)
	}
	// Unreadable stages do not make the project disappear: the rest of its description
	// is right, and a project missing from the screen would be looked for in the forge
	// before being looked for in the database.
	if len(targets) > 0 {
		_ = json.Unmarshal(targets, &project.Targets)
	}
	// An unreadable cycle falls back to the original one rather than leaving the project
	// with no creatable kind: the column is constrained non-empty, and a decoding
	// failure names a flaw in the dashboard, not in the declaration.
	if len(kinds) > 0 {
		_ = json.Unmarshal(kinds, &project.ReleaseKinds)
	}
	if len(project.ReleaseKinds) == 0 {
		project.ReleaseKinds = release.DefaultCycle()
	}
	return project, nil
}

// CreateProject declares a repository.
func (db *DB) CreateProject(ctx context.Context, project Project) (int64, error) {
	targets, err := json.Marshal(normaliseTargets(project.Targets))
	if err != nil {
		return 0, fmt.Errorf("serialising the stages: %w", err)
	}

	if strings.TrimSpace(project.Slug) == "" {
		project.Slug = SlugFor(project.Repository)
	}

	// The cycle is serialised here rather than left to the caller: the column is
	// constrained non-empty, and a project declared with no kind would be refused by the
	// database with a message naming nothing useful.
	cycle := project.ReleaseKinds
	if len(cycle) == 0 {
		cycle = release.DefaultCycle()
	}
	kinds, err := json.Marshal(cycle)
	if err != nil {
		return 0, fmt.Errorf("serialising the cycle: %w", err)
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
			// `do nothing` returned nothing: the repository is already declared.
			return 0, ErrProjectExists
		}
		return 0, fmt.Errorf("declaring project %q: %w", project.Repository, err)
	}
	return id, nil
}

// UpdateProject replaces a project's description, repository included.
//
// The repository was frozen at first, on the grounds that changing it would make a
// project another one. That was true and beside the point: bootstrapped projects
// carry the repository the configuration named, and correcting that one is exactly
// what this is for — freezing it forced a delete and redeclare, losing everything
// that accompanied the row.
//
// What was right in that reasoning is kept: changing repository clears the access
// verdict. It was about something no longer driven, and keeping it would display
// "access verified" on a repository nobody tested — the worst of the three possible
// answers.
func (db *DB) UpdateProject(ctx context.Context, project Project) error {
	// An empty repository is never an intention: it is a caller that forgot the field.
	// Letting it through would clear it silently, and the project would become a row
	// naming nothing — which no screen could explain.
	if strings.TrimSpace(project.Repository) == "" {
		return fmt.Errorf("updating project %d: empty repository", project.ID)
	}

	targets, err := json.Marshal(normaliseTargets(project.Targets))
	if err != nil {
		return fmt.Errorf("serialising the stages: %w", err)
	}

	if strings.TrimSpace(project.Slug) == "" {
		project.Slug = SlugFor(project.Repository)
	}

	// The cycle is serialised here rather than left to the caller: the column is
	// constrained non-empty, and a project declared with no kind would be refused by the
	// database with a message naming nothing useful.
	cycle := project.ReleaseKinds
	if len(cycle) == 0 {
		cycle = release.DefaultCycle()
	}
	kinds, err := json.Marshal(cycle)
	if err != nil {
		return fmt.Errorf("serialising the cycle: %w", err)
	}

	tag, err := db.pool.Exec(ctx,
		`update projects
		 set repository = $2, label = $3, forge_url = $4, dockerfile = $5,
		     build_context = $6, image_registry = $7, multi_stage = $8,
		     targets = $9, slug = $10, tracker_project = $11, registry_secret = $12,
		     version_scheme = coalesce(nullif($13, ''), 'semver'),
		     release_kinds = $14, stack = $15,
		     -- The verdict survives only an unchanged repository. The comparison
		     -- is on the stored value, still readable during the update: a prior
		     -- read would open a window between the verdict and the write.
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
		// A repository already declared elsewhere: two entries would describe it, and
		// nothing would say which prevails. The caller must be able to say so rather than
		// return a fault.
		//
		// The error code rather than the message: the message is translated to the server's
		// locale, and comparing it would work here and stop working on a database configured
		// otherwise.
		var violation *pgconn.PgError
		if errors.As(err, &violation) && violation.Code == uniqueViolation {
			return ErrProjectExists
		}
		return fmt.Errorf("updating project %d: %w", project.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// DeleteProject removes a repository from the list.
func (db *DB) DeleteProject(ctx context.Context, id int64) error {
	tag, err := db.pool.Exec(ctx, `delete from projects where id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting project %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// RecordProjectCheck records the result of an access check.
//
// Kept rather than recomputed on display: the call leaves the dashboard, and redoing
// it every time the screen opens would pay a forge round trip for information that
// changes once a quarter.
func (db *DB) RecordProjectCheck(ctx context.Context, id int64, ok bool, note string) error {
	tag, err := db.pool.Exec(ctx,
		`update projects
		 set checked_at = now(), check_ok = $2, check_note = $3, updated_at = now()
		 where id = $1`, id, ok, note)
	if err != nil {
		return fmt.Errorf("access verdict of project %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// normaliseTargets discards empty stages.
//
// A form readily leaves a half-filled row behind. Saving it would give a stage with
// no name, which no build could build and which nothing on screen would tell from a
// valid one.
func normaliseTargets(targets []Target) []Target {
	kept := make([]Target, 0, len(targets))
	for _, target := range targets {
		// The image alone is enough: a stage may have neither a target nor its own
		// Dockerfile — that is a single-Dockerfile repository — but with no image name it
		// names nothing to publish.
		if strings.TrimSpace(target.Image) == "" {
			continue
		}
		kept = append(kept, target)
	}
	return kept
}

// SetDeployKey records the public half of a project's key.
//
// Written after the Secret has been placed in the cluster, never before: this column
// is the ownership record, and writing it first would suggest a key was set if
// placing it then failed. A later rotation would then replace a Secret that does not
// exist, or worse, someone else's.
func (db *DB) SetDeployKey(ctx context.Context, projectID int64, public string) error {
	_, err := db.pool.Exec(ctx,
		// The rotation request is cleared in the same gesture: leaving it would set a new key
		// on every turn of the loop.
		`update projects
		 set deploy_key_public = $2, deploy_key_created_at = now(),
		     deploy_key_rotate_requested_at = null, updated_at = now()
		 where id = $1`, projectID, public)
	if err != nil {
		return fmt.Errorf("key of project %d: %w", projectID, err)
	}
	return nil
}

// ClearDeployKey forgets a project's key.
//
// It returns the project to the "no key" state: a later set will refuse to adopt a
// Secret of the same name, which is the intended behaviour — having forgotten the
// key, one can no longer assert the Secret is ours.
func (db *DB) ClearDeployKey(ctx context.Context, projectID int64) error {
	_, err := db.pool.Exec(ctx,
		`update projects
		 set deploy_key_public = '', deploy_key_created_at = null, updated_at = now()
		 where id = $1`, projectID)
	if err != nil {
		return fmt.Errorf("forgetting the key of project %d: %w", projectID, err)
	}
	return nil
}

// DeployKeySecret returns the name of the secret carrying a project's key.
//
// False when the project has no key of its own. The answer is read from
// `deploy_key_public` and not from the cluster: the dashboard's RBAC does not let it
// read a secret there, and this column is authoritative about ownership.
func (db *DB) DeployKeySecret(ctx context.Context, projectID int64) (string, bool, error) {
	var slug, public string
	err := db.pool.QueryRow(ctx,
		// `deploy_key_active` and not merely the key's presence: between setting it and the
		// moment someone pastes it into the forge, it opens nothing. Using it regardless
		// would fail the clone of a repository that worked the day before.
		`select slug, deploy_key_public from projects
		 where id = $1 and deploy_key_active`, projectID).
		Scan(&slug, &public)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("key of project %d: %w", projectID, err)
	case public == "":
		return "", false, nil
	}
	return deploykeys.SecretName(slug), true, nil
}

// RequestKeyRotation asks for a project's key to be replaced.
//
// A request and not a replacement: the API runs without a cluster token, and giving
// it one for the convenience of a synchronous set would be a security regression —
// it is the exposed surface. The worker, which already has the cluster and the
// leader lease, carries it out.
func (db *DB) RequestKeyRotation(ctx context.Context, projectID int64) error {
	tag, err := db.pool.Exec(ctx,
		`update projects set deploy_key_rotate_requested_at = now(), updated_at = now()
		 where id = $1`, projectID)
	if err != nil {
		return fmt.Errorf("rotation request for project %d: %w", projectID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// PendingKey describes a project whose key is to be set.
type PendingKey struct {
	ID   int64
	Slug string
	// Known says whether the dashboard already holds this project's key. That is what
	// separates a rotation — the Secret is ours, we replace it — from a first set, where
	// a Secret of the same name necessarily came from elsewhere and must not be adopted.
	Known bool
}

// ProjectsAwaitingKey returns the projects still to be provided for.
//
// Two cases together, because the loop treats them the same way: the one with no key
// at all, and the one whose rotation was requested. A restart after a failure catches
// up on both without having to tell which had been interrupted.
func (db *DB) ProjectsAwaitingKey(ctx context.Context) ([]PendingKey, error) {
	rows, err := db.pool.Query(ctx,
		`select id, slug, deploy_key_public <> ''
		 from projects
		 where deploy_key_public = '' or deploy_key_rotate_requested_at is not null
		 order by id`)
	if err != nil {
		return nil, fmt.Errorf("projects without a key: %w", err)
	}
	defer rows.Close()

	var pending []PendingKey
	for rows.Next() {
		var entry PendingKey
		if err := rows.Scan(&entry.ID, &entry.Slug, &entry.Known); err != nil {
			return nil, fmt.Errorf("projects without a key: %w", err)
		}
		pending = append(pending, entry)
	}
	return pending, rows.Err()
}

// ActivateDeployKey records that a project's key opens its repository.
//
// An observation, not a declaration. The only caller is the access check, which has
// just read the forge's deploy keys and found — or not — the project's. Pasting the
// public half stays a human gesture; confirming it no longer is.
//
// The distinction has a concrete effect: a manual confirmation stayed true after the
// key had been removed from the repository, and pipelines then failed on an SSH
// error that nothing connected to the screen asserting the contrary.
func (db *DB) ActivateDeployKey(ctx context.Context, projectID int64, active bool) error {
	tag, err := db.pool.Exec(ctx,
		`update projects set deploy_key_active = $2, updated_at = now() where id = $1`,
		projectID, active)
	if err != nil {
		return fmt.Errorf("activating the key of project %d: %w", projectID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// RegistrySecret returns the registry secret a project names.
//
// False when it names none: the run then uses the deployment's, which is the case of
// every project publishing to the same place.
func (db *DB) RegistrySecret(ctx context.Context, projectID int64) (string, bool, error) {
	var name string
	err := db.pool.QueryRow(ctx,
		`select registry_secret from projects where id = $1`, projectID).Scan(&name)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("registry secret of project %d: %w", projectID, err)
	case name == "":
		return "", false, nil
	}
	return name, true, nil
}
