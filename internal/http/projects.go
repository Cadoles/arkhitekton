package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/pipelines"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/version"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

/*
 * The repositories the dashboard drives.
 *
 * They lived in the configuration: declaring one more required modifying a ConfigMap, hence
 * rebuilding and redeploying. Nothing said, from the interface, which repositories the dashboard
 * believed it was driving — and above all, nothing allowed checking that it had access to them.
 * The gap was discovered only at the clone, mid-pipeline, in a message that did not name it.
 *
 * The table describes; it does not drive yet. The pipelines keep receiving their repositories by
 * configuration, and wiring them up will require settling what the scheduler accepts from a
 * request.
 */

// ProjectStore is what the handler needs from the database.
type ProjectStore interface {
	ListProjects(ctx context.Context) ([]store.Project, error)
	GetProject(ctx context.Context, id int64) (store.Project, error)
	CreateProject(ctx context.Context, project store.Project) (int64, error)
	UpdateProject(ctx context.Context, project store.Project) error
	DeleteProject(ctx context.Context, id int64) error
	RecordProjectCheck(ctx context.Context, id int64, ok bool, note string) error
	RequestKeyRotation(ctx context.Context, projectID int64) error
	ActivateDeployKey(ctx context.Context, projectID int64, active bool) error
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// ProjectProber checks that a repository is reachable.
//
// Extracted as an interface so the handler can be exercised without a forge: what
// deserves testing here is turning a failure into a readable verdict, not the HTTP
// client that produces it.
type ProjectProber interface {
	Probe(ctx context.Context, repository, publicKey string) (provider.Probe, error)
}

// ProjectsHandler exposes the declaration of repositories.
type ProjectsHandler struct {
	store ProjectStore
	// prober tests access. Nil when the forge is not configured: the check then answers
	// that it cannot conclude, which beats a failure that would be read as an access
	// problem.
	prober ProjectProber
	// schemeCheck refuses a version scheme a plugin could not carry. Nil: any scheme is
	// acceptable, since nothing promotes.
	//
	// A function and not a deployer: the core need not know who promotes, only that a
	// project's configuration may be refused by whoever does.
	schemeCheck func(context.Context, version.Scheme) error
	forgeURL    string
	// imageRegistry is the registry offered by default when declaring a project. The
	// configuration's: that is where existing projects publish, and retyping it on every
	// declaration would invite a typo.
	imageRegistry string
	// library knows the stacks the plugins bring a reference library for. Nil: every declared
	// stack is refused.
	library *pipelines.Library
}

// WithLibrary wires the stacks a project may declare.
func (h *ProjectsHandler) WithLibrary(library *pipelines.Library) *ProjectsHandler {
	h.library = library
	return h
}

func NewProjectsHandler(projects ProjectStore) *ProjectsHandler {
	return &ProjectsHandler{store: projects}
}

// WithSchemeCheck wires the refusal of schemes a plugin cannot carry.
func (h *ProjectsHandler) WithSchemeCheck(check func(context.Context, version.Scheme) error) *ProjectsHandler {
	h.schemeCheck = check
	return h
}

// WithProbe wires the access test.
func (h *ProjectsHandler) WithProbe(prober ProjectProber) *ProjectsHandler {
	h.prober = prober
	return h
}

// WithForgeURL declares the forge offered by default when declaring a project.
//
// It replaces WithDeployKey, which also carried the public half of a key shared by
// every project. That no longer exists: each project has its own, set by the worker,
// and its presence in the repository is observed by the access check. One key added to
// several repositories made its revocation an outage for everyone, and said nothing
// about who cloned what.
func (h *ProjectsHandler) WithForgeURL(forgeURL string) *ProjectsHandler {
	h.forgeURL = strings.TrimRight(forgeURL, "/")
	return h
}

// WithImageRegistry declares the registry offered by default.
func (h *ProjectsHandler) WithImageRegistry(registry string) *ProjectsHandler {
	h.imageRegistry = strings.Trim(strings.TrimSpace(registry), "/")
	return h
}

type targetPayload struct {
	Stage string `json:"stage"`
	Image string `json:"image"`
	// Dockerfile specific to this image, when it differs from the project's.
	Dockerfile string `json:"dockerfile,omitempty"`
	// Promote says whether a re-tag propagates this image. Always returned, never omitted:
	// an absent boolean reads as false on the form side, which would say "not promoted"
	// about a stage nobody has pronounced on.
	Promote bool `json:"promote"`
	// Reference is the full reference, registry included. Composed by the server: the
	// screen shows it so it can be recognised, and recomposing it in the browser would let
	// it diverge from what the pipeline will receive.
	Reference string `json:"reference,omitempty"`
}

type projectPayload struct {
	ID           int64  `json:"id"`
	Repository   string `json:"repository"`
	Label        string `json:"label"`
	ForgeURL     string `json:"forge_url"`
	URL          string `json:"url"`
	Dockerfile   string `json:"dockerfile"`
	BuildContext string `json:"build_context"`
	// Slug names the project in URLs: `/projects/mse/releases/1.8.0`. Returned and
	// editable — these addresses end up bookmarked, and a name one cannot correct is a name
	// one endures.
	Slug          string `json:"slug"`
	ImageRegistry string `json:"image_registry"`
	// RegistrySecret names the secret allowing pushes to this registry. Empty means the
	// deployment's.
	//
	// A name, never a credential. The secret itself is provisioned by the deployment, and
	// the dashboard merely names it: letting a registry password in through this form would
	// route it through the browser and the API.
	RegistrySecret string `json:"registry_secret"`
	// VersionScheme names the shape of the tags. An opaque scheme stays usable: it loses
	// the next-version suggestion and range selection, and the screen must say so rather
	// than compute something arbitrary.
	VersionScheme string `json:"version_scheme"`
	// Stack names the stack whose reference library is installed. Empty: the project brings
	// its own pipelines, which is perfectly valid — the normative part is the contract, not
	// the library.
	Stack string `json:"stack"`
	// TrackerProject names this project in the tracking tool. Empty: no tracker, and its
	// versions are declared by hand.
	TrackerProject string          `json:"tracker_project"`
	MultiStage     bool            `json:"multi_stage"`
	Targets        []targetPayload `json:"targets"`
	// CheckedAt absent: no check has happened. A state distinct from a check that failed,
	// and which must not read like one.
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	CheckOK   *bool      `json:"check_ok,omitempty"`
	CheckNote string     `json:"check_note,omitempty"`
	// DeployKey is the public half of the project's own key. Empty while the worker has not
	// set it.
	//
	// The private half never appears here, nor anywhere else. It is written once into a
	// Secret and does not come back out.
	DeployKey          string     `json:"deploy_key,omitempty"`
	DeployKeyCreatedAt *time.Time `json:"deploy_key_created_at,omitempty"`
	// DeployKeyActive says the key really opens the repository: its public half appears
	// among the forge's deploy keys.
	//
	// Observed by the access check, not declared by a human: a manual confirmation stayed
	// true after the key had been removed from the repository, and pipelines then failed on
	// an SSH error that nothing connected to the screen asserting the contrary.
	//
	// False, nothing builds. There is no shared key to fall back on any more: it made its
	// revocation an outage for every project at once, and said nothing about who cloned
	// what.
	DeployKeyActive bool   `json:"deploy_key_active"`
	CreatedBy       string `json:"created_by"`
}

// List returns the projects, the deploy key and the default forge.
//
// All three together: the screen has no use for one without the others, and the key is
// what one comes looking for when declaring a project.
func (h *ProjectsHandler) List(w http.ResponseWriter, r *http.Request) {
	projects, err := h.store.ListProjects(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the projects", "")
		return
	}

	payload := make([]projectPayload, 0, len(projects))
	for _, project := range projects {
		payload = append(payload, h.toPayload(project))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"projects":  payload,
		"forge_url": h.forgeURL,
		// Offered when declaring: that is where existing projects publish.
		"image_registry": h.imageRegistry,
		// The check proves only the dashboard's access, by token and over HTTPS. Pipelines
		// clone over SSH with the deploy key, and nothing here exercises that: saying so avoids
		// reading a green check as a guarantee that a clone will work.
		"probe_configured": h.prober != nil,
	})
}

func (h *ProjectsHandler) toPayload(project store.Project) projectPayload {
	targets := make([]targetPayload, 0, len(project.Targets))
	for _, target := range project.Targets {
		targets = append(targets, targetPayload{
			Stage: target.Stage, Image: target.Image,
			Dockerfile: target.Dockerfile, Promote: target.Promoted(),
			Reference: project.Reference(target),
		})
	}

	return projectPayload{
		ID: project.ID, Repository: project.Repository, Label: project.Label,
		ForgeURL: project.ForgeURL, URL: repositoryURL(project.ForgeURL, project.Repository),
		Dockerfile: project.Dockerfile, BuildContext: project.BuildContext,
		Slug:           project.Slug,
		ImageRegistry:  project.ImageRegistry,
		RegistrySecret: project.RegistrySecret,
		VersionScheme:  project.VersionScheme,
		Stack:          project.Stack,
		TrackerProject: project.TrackerProject,
		MultiStage:     project.MultiStage, Targets: targets,
		CheckedAt: project.CheckedAt, CheckOK: project.CheckOK,
		CheckNote: project.CheckNote, CreatedBy: project.CreatedBy,
		DeployKey: project.DeployKeyPublic, DeployKeyCreatedAt: project.DeployKeyCreatedAt,
		DeployKeyActive: project.DeployKeyActive,
	}
}

// repositoryURL composes the link to the repository. Empty if either half is missing: a
// truncated link leads to the forge without leading to the repository.
func repositoryURL(forgeURL, repository string) string {
	forge := strings.TrimRight(forgeURL, "/")
	if forge == "" || repository == "" {
		return ""
	}
	return forge + "/" + repository
}

type projectRequest struct {
	Repository   string `json:"repository"`
	Label        string `json:"label"`
	ForgeURL     string `json:"forge_url"`
	Dockerfile   string `json:"dockerfile"`
	BuildContext string `json:"build_context"`
	// Slug names the project in URLs: `/projects/mse/releases/1.8.0`. Returned and
	// editable — these addresses end up bookmarked, and a name one cannot correct is a name
	// one endures.
	Slug           string `json:"slug"`
	ImageRegistry  string `json:"image_registry"`
	RegistrySecret string `json:"registry_secret"`
	// VersionScheme names the shape of this project's tags. Empty means `semver`.
	VersionScheme string `json:"version_scheme"`
	// Stack names the stack whose reference library will be installed. Empty: the project
	// brings its own pipelines.
	Stack string `json:"stack"`
	// TrackerProject names this project in the tracking tool. Empty: no tracker, and its
	// versions are declared by hand.
	//
	// Absent from this form until now, although the column has existed since
	TrackerProject string          `json:"tracker_project"`
	MultiStage     bool            `json:"multi_stage"`
	Targets        []targetPayload `json:"targets"`
}

// repositoryPattern bounds the shape of a repository.
//
// `owner/name`, which the forge API expects. Checking it here rather than at the first
// call avoids an access check failing on a malformed URL and reading as a rights
// problem.
const repositorySeparator = "/"

func (h *ProjectsHandler) decode(w http.ResponseWriter, r *http.Request) (projectRequest, bool) {
	var request projectRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request", "")
		return request, false
	}

	request.Repository = strings.Trim(strings.TrimSpace(request.Repository), "/")
	owner, name, found := strings.Cut(request.Repository, repositorySeparator)
	if !found || owner == "" || name == "" || strings.Contains(name, repositorySeparator) {
		writeProblem(w, http.StatusBadRequest, "Invalid repository", "expected owner/name")
		return request, false
	}

	request.Label = strings.TrimSpace(request.Label)
	if request.Label == "" {
		// The repository's name makes an acceptable label, and requiring one would stumble over
		// a field whose value is obvious.
		request.Label = name
	}

	request.ForgeURL = strings.TrimRight(strings.TrimSpace(request.ForgeURL), "/")
	if request.ForgeURL == "" {
		request.ForgeURL = h.forgeURL
	}

	request.BuildContext = strings.TrimSpace(request.BuildContext)
	if request.BuildContext == "" {
		request.BuildContext = "."
	}
	request.Dockerfile = strings.TrimSpace(request.Dockerfile)
	// Trailing slashes are stripped: composing the reference would double them, and
	// `registry/project//image` is not the same image as `registry/project/image` to a
	// registry.
	request.ImageRegistry = strings.Trim(strings.TrimSpace(request.ImageRegistry), "/")
	if request.ImageRegistry == "" {
		request.ImageRegistry = h.imageRegistry
	}

	// The scheme × deployer coupling is refused here, at configuration time, and not
	// discovered at the first deployment that does not leave.
	//
	// It is the failure mode of a GitOps controller selecting by SemVer range: with an
	// opaque scheme the promotion tag would be pushed, accepted by the registry, ignored by
	// the controller — the promotion would look successful and nothing would move.
	request.VersionScheme = strings.TrimSpace(request.VersionScheme)
	scheme, err := version.For(request.VersionScheme)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Unknown version scheme", err.Error())
		return request, false
	}
	if h.schemeCheck != nil {
		if err := h.schemeCheck(r.Context(), scheme); err != nil {
			writeProblem(w, http.StatusBadRequest,
				"Version scheme incompatible with the deployer", err.Error())
			return request, false
		}
	}

	// The stack is refused at declaration time if no library covers it. Learning it here
	// rather than at the first launch avoids finding a pipeline missing without knowing
	// why — and the message says what to do: a project whose stack is not covered writes its
	// own.
	request.Stack = strings.TrimSpace(request.Stack)
	if _, err := h.library.For(request.Stack); err != nil {
		writeProblem(w, http.StatusBadRequest, "Unknown stack", err.Error())
		return request, false
	}

	return request, true
}

func targetsOf(request projectRequest) []store.Target {
	targets := make([]store.Target, 0, len(request.Targets))
	for _, target := range request.Targets {
		// Written explicitly, including when it is true: the form settled it, and leaving the
		// entry silent would make it indistinguishable from those the migration caught up.
		promote := target.Promote
		targets = append(targets, store.Target{
			Stage:      strings.TrimSpace(target.Stage),
			Image:      strings.TrimSpace(target.Image),
			Dockerfile: strings.TrimSpace(target.Dockerfile),
			Promote:    &promote,
		})
	}
	return targets
}

// Create declares a repository.
func (h *ProjectsHandler) Create(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}

	request, ok := h.decode(w, r)
	if !ok {
		return
	}

	id, err := h.store.CreateProject(r.Context(), store.Project{
		Repository: request.Repository, Label: request.Label, ForgeURL: request.ForgeURL,
		Dockerfile: request.Dockerfile, BuildContext: request.BuildContext,
		Slug:           strings.TrimSpace(request.Slug),
		ImageRegistry:  request.ImageRegistry,
		RegistrySecret: strings.TrimSpace(request.RegistrySecret),
		VersionScheme:  strings.TrimSpace(request.VersionScheme),
		Stack:          strings.TrimSpace(request.Stack),
		TrackerProject: strings.TrimSpace(request.TrackerProject),
		MultiStage:     request.MultiStage, Targets: targetsOf(request),
		CreatedBy: identity.Subject,
	})
	if err != nil {
		if errors.Is(err, store.ErrProjectExists) {
			writeProblem(w, http.StatusConflict, "Repository already declared", request.Repository)
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot declare", "")
		return
	}

	_ = h.store.Audit(r.Context(), identity.Subject, store.AuditProjectSet, request.Repository,
		map[string]any{"label": request.Label})

	// The project's key is set by the worker, which alone talks to the cluster. The project
	// is usable immediately: meanwhile its pipelines mount the deployment's shared key.
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// RotateDeployKey asks for a project's key to be replaced.
//
// A request, carried out by the worker. The API runs without a cluster token —
// `automountServiceAccountToken: false`, deliberately, because it is the exposed
// surface — and granting it one for the convenience of a synchronous set would be a
// security regression disguised as a feature.
//
// The old key stays valid until the new one is set: rotation therefore does not break
// clones in flight, and the screen says it is requested rather than done.
func (h *ProjectsHandler) RotateDeployKey(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}

	id, ok := projectID(w, r)
	if !ok {
		return
	}

	if err := h.store.RequestKeyRotation(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeProblem(w, http.StatusNotFound, "Project not found", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot request", "")
		return
	}

	_ = h.store.Audit(r.Context(), identity.Subject, store.AuditProjectSet,
		strconv.FormatInt(id, 10), map[string]any{"deploy_key": "rotation requested"})

	// 202 and not 200: the key is not replaced yet, and answering 200 would suggest it can
	// already be pasted into the forge.
	writeJSON(w, http.StatusAccepted, map[string]any{"requested": true})
}

// Update replaces a project's description.
func (h *ProjectsHandler) Update(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}

	id, ok := projectID(w, r)
	if !ok {
		return
	}

	request, ok := h.decode(w, r)
	if !ok {
		return
	}

	// The repository is editable, and that is the main use case: bootstrapped projects
	// carry the one the configuration named, and correcting it is exactly what this is for.
	// Changing it clears the access verdict — it was about something no longer driven.
	err := h.store.UpdateProject(r.Context(), store.Project{
		ID: id, Repository: request.Repository, Label: request.Label,
		ForgeURL: request.ForgeURL, Dockerfile: request.Dockerfile,
		BuildContext: request.BuildContext, ImageRegistry: request.ImageRegistry,
		RegistrySecret: strings.TrimSpace(request.RegistrySecret),
		VersionScheme:  strings.TrimSpace(request.VersionScheme),
		Stack:          strings.TrimSpace(request.Stack),
		TrackerProject: strings.TrimSpace(request.TrackerProject),
		Slug:           strings.TrimSpace(request.Slug),
		MultiStage:     request.MultiStage, Targets: targetsOf(request),
	})
	if err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeProblem(w, http.StatusNotFound, "Project not found", "")
			return
		}
		if errors.Is(err, store.ErrProjectExists) {
			writeProblem(w, http.StatusConflict, "Repository already declared", request.Repository)
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot update", "")
		return
	}

	_ = h.store.Audit(r.Context(), identity.Subject, store.AuditProjectSet, request.Repository,
		map[string]any{"label": request.Label, "id": id})

	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// Delete removes a repository from the list.
func (h *ProjectsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}

	id, ok := projectID(w, r)
	if !ok {
		return
	}

	// The repository is read back before being removed: the audit log must name what
	// disappeared, and after the deletion there is nothing left to name.
	project, err := h.store.GetProject(r.Context(), id)
	if err != nil && !errors.Is(err, store.ErrProjectNotFound) {
		writeProblem(w, http.StatusInternalServerError, "Cannot delete", "")
		return
	}

	if err := h.store.DeleteProject(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeProblem(w, http.StatusNotFound, "Project not found", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot delete", "")
		return
	}

	_ = h.store.Audit(r.Context(), identity.Subject, store.AuditProjectRemove, project.Repository, nil)

	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// Probe checks that the dashboard reaches the repository.
//
// What the check proves, and nothing more: the dashboard reads this repository through
// the forge API, with its token, over HTTPS. The pipelines clone over SSH with the
// deploy key, from a pod that is not this one. A green check therefore does not
// guarantee a clone will work — that is said on screen, failing which a forgotten key
// would surface at the first build.
func (h *ProjectsHandler) Probe(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}

	id, ok := projectID(w, r)
	if !ok {
		return
	}

	project, err := h.store.GetProject(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeProblem(w, http.StatusNotFound, "Project not found", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot test", "")
		return
	}

	if h.prober == nil {
		// Neither green nor red: the check did not happen. Recording it as a failure would send
		// someone hunting for an access problem where there is only a missing token.
		writeProblem(w, http.StatusServiceUnavailable, "Test unavailable",
			"the forge is not configured: GITEA_URL and GITEA_TOKEN are required")
		return
	}

	probe, probeErr := h.prober.Probe(r.Context(), project.Repository, project.DeployKeyPublic)
	note := ""
	if probeErr != nil {
		note = probeErr.Error()
	} else {
		note = "default branch: " + probe.DefaultBranch
	}

	if err := h.store.RecordProjectCheck(r.Context(), id, probeErr == nil, note); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot record the check", "")
		return
	}

	// The key's fate, observed and not declared.
	//
	// It used to be confirmed by hand: someone pasted the public half into the repository's
	// settings, then came to say so. An intention, therefore, with the flaw of intentions —
	// it stayed true after the key had been removed from the repository, and the pipelines
	// failed on an SSH error that nothing connected to the screen asserting the contrary.
	//
	// Silence is respected: when the question could not be asked, the previous state is
	// left as it is. Overwriting it to false would cut the builds of a project whose key
	// may well be in place, for the sole reason that the token may not read the
	// repository's keys.
	if probe.KeyPresent != nil {
		if err := h.store.ActivateDeployKey(r.Context(), id, *probe.KeyPresent); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Cannot check the key", "")
			return
		}
	}

	_ = h.store.Audit(r.Context(), identity.Subject, store.AuditProjectProbe, project.Repository,
		map[string]any{"ok": probeErr == nil, "cle_presente": probe.KeyPresent})

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   probeErr == nil,
		"note": note,
		// Three states returned as they are: null means "the question could not be asked",
		// which the screen must say differently from an absence.
		"key_present": probe.KeyPresent,
		"key_note":    probe.KeyNote,
	})
}

func projectID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid identifier", "")
		return 0, false
	}
	return id, true
}
