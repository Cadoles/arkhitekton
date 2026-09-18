package http

import (
	"net/http"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
)

// NewAPIRouter mounts the application routes.
//
// Authorization is decided here, route by route, and never in a handler: a scattered
// check always ends up missing on a route added later. The table below is therefore the
// reference for what is protected. See docs/07-http-api.md.
func NewAPIRouter(
	handler *AuthHandler, runs *RunsHandler, logs *LogsHandler, queue *QueueHandler,
	releases *ReleasesHandler, benches *BenchHandler, tests *TestsHandler,
	versions *VersionsHandler, syncAdmin *SyncAdminHandler,
	forge *ForgeHandler, admin *AdminHandler, projects *ProjectsHandler,
	plugins *PluginsHandler, projectPipelines *ProjectPipelinesHandler,
	events *EventsHandler, reactions *AutomationsHandler, planRuns *PlanRunsHandler,
	middleware *Middleware, mounts ...Mount,
) http.Handler {
	mux := http.NewServeMux()

	// Routes carrying objects attached to a project — releases and versions — name it in
	// their path. Their identity exists only within a project, and a URL that does not say
	// so would name two things the day a second one exists.
	//
	// The queue, the runs and the environments keep their path. That is not an oversight:
	// nothing in the database attaches them to a project, and prefixing them would settle
	// sideways questions that are not — is a queue project-specific when its ceilings are the
	// cluster's? is a qualification environment shared?

	// Public routes: they exist precisely to obtain a session.
	mux.HandleFunc("GET /auth/login", handler.Login)
	mux.HandleFunc("GET /auth/callback", handler.Callback)

	// Signing out modifies state, so it goes through the CSRF check, but requires no role: a
	// user with no role must be able to sign out.
	mux.Handle("POST /auth/logout", Chain(http.HandlerFunc(handler.Logout), middleware.CSRF))

	mux.Handle("GET /me", Chain(http.HandlerFunc(handler.Me), middleware.Authenticate))

	// Runs are readable by every role: observing what the pipelines did requires no
	// privilege, and it is the dashboard's first genuinely useful purpose.
	mux.Handle("GET /runs", Chain(http.HandlerFunc(runs.List),
		middleware.Authenticate, middleware.Require(auth.PermRead)))
	mux.Handle("GET /runs/events", Chain(http.HandlerFunc(runs.Stream),
		middleware.Authenticate, middleware.Require(auth.PermRead)))
	// Declared after /runs/events: the ServeMux picks the most specific pattern, but reading
	// order stays clearer this way.
	mux.Handle("GET /runs/{run}", Chain(http.HandlerFunc(runs.Get),
		middleware.Authenticate, middleware.Require(auth.PermRead)))

	// Logs are read by range, and followed by stream. Both go through the same reader: a
	// client can follow a run and then go back through its history without changing
	// mechanism.
	mux.Handle("GET /runs/{run}/tasks/{task}/log", Chain(http.HandlerFunc(logs.Read),
		middleware.Authenticate, middleware.Require(auth.PermRead)))
	mux.Handle("GET /runs/{run}/tasks/{task}/log/stream", Chain(http.HandlerFunc(logs.Stream),
		middleware.Authenticate, middleware.Require(auth.PermRead)))
	// The whole log, as text: viewable in a tab, saveable as a file. The same permission as
	// the ranged read — it is the same data, served otherwise.
	mux.Handle("GET /runs/{run}/tasks/{task}/log.txt", Chain(http.HandlerFunc(logs.Raw),
		middleware.Authenticate, middleware.Require(auth.PermRead)))

	// The queue is readable by all: understanding why a run is waiting requires no
	// privilege, and that is precisely what avoids needless retries.
	mux.Handle("GET /queue", Chain(http.HandlerFunc(queue.List),
		middleware.Authenticate, middleware.Require(auth.PermRead)))

	// Launching and cancelling modify state: a CSRF check in addition to the permission, and
	// `pipeline:run` rather than `read`.
	mux.Handle("POST /queue", Chain(http.HandlerFunc(queue.Launch),
		middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermPipelineRun)))
	// Queue ceilings are an operational setting: raising one commits the cluster, lowering it
	// lengthens every verification. Reserved to administration, like roles.
	mux.Handle("PUT /queue/limits/{kind}", Chain(http.HandlerFunc(queue.SetLimit),
		middleware.Authenticate, middleware.Require(auth.PermAdmin), middleware.CSRF))
	mux.Handle("DELETE /queue/limits/{kind}", Chain(http.HandlerFunc(queue.ClearLimit),
		middleware.Authenticate, middleware.Require(auth.PermAdmin), middleware.CSRF))

	mux.Handle("POST /queue/{id}/cancel", Chain(http.HandlerFunc(queue.Cancel),
		middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermPipelineCancel)))

	// Releases are read like runs.
	mux.Handle("GET /projects/{project}/releases", Chain(http.HandlerFunc(releases.List),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))
	mux.Handle("GET /projects/{project}/releases/{tag}", Chain(http.HandlerFunc(releases.Get),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))

	// The route requires `release:create`; creating a final release requires more, and the
	// handler checks it — the kind is in the body, not in the path, and a middleware cannot
	// read it without consuming the request.
	mux.Handle("POST /projects/{project}/releases", Chain(http.HandlerFunc(releases.Create),
		middleware.Authenticate, middleware.CSRF, middleware.RequireIn(auth.PermReleaseCreate)))

	// Approving a release commits what will reach the user: the same permission as producing
	// a final release, and not `release:edit`. It is also the only transition nothing
	// automates — a green pipeline says the code works, not that anyone wants to ship it.
	mux.Handle("POST /projects/{project}/releases/{tag}/release", Chain(http.HandlerFunc(releases.Release),
		middleware.Authenticate, middleware.CSRF, middleware.RequireIn(auth.PermReleaseFinal)))

	// Relaunching a build creates no version: the same permission as creation, and not that
	// of shipping. It is the gesture that repairs an infrastructure failure without making
	// the version number carry the history of the cluster's outages.
	mux.Handle("POST /projects/{project}/releases/{tag}/build", Chain(http.HandlerFunc(releases.Rebuild),
		middleware.Authenticate, middleware.CSRF, middleware.RequireIn(auth.PermReleaseCreate)))

	// Re-running a single verification. The same permission as a rebuild: it is computation
	// in the cluster, not a delivery. Less committing, even: nothing is republished, the
	// image does not move.
	mux.Handle("POST /projects/{project}/releases/{tag}/verify/{kind}", Chain(http.HandlerFunc(releases.Verify),
		middleware.Authenticate, middleware.CSRF, middleware.RequireIn(auth.PermReleaseCreate)))

	// The release note is read with the release and edited like the manual actions:
	// `release:edit` and not `release:create:final`. Composing the note is the work of
	// whoever produces the deliverable, not of whoever decides to ship it.
	mux.Handle("GET /projects/{project}/releases/{tag}/notes", Chain(http.HandlerFunc(releases.Notes),
		middleware.Authenticate))
	mux.Handle("POST /projects/{project}/releases/{tag}/notes/{issue}", Chain(http.HandlerFunc(releases.AddNote),
		middleware.Authenticate, middleware.CSRF, middleware.RequireIn(auth.PermReleaseEdit)))
	mux.Handle("DELETE /projects/{project}/releases/{tag}/notes/{issue}", Chain(http.HandlerFunc(releases.RemoveNote),
		middleware.Authenticate, middleware.CSRF, middleware.RequireIn(auth.PermReleaseEdit)))

	// The manual actions are editable by those who produce the deliverable, not only by
	// those who can ship: `release:edit` and not `release:create:final`.
	mux.Handle("PUT /projects/{project}/releases/{tag}/actions", Chain(http.HandlerFunc(releases.UpdateActions),
		middleware.Authenticate, middleware.CSRF, middleware.RequireIn(auth.PermReleaseEdit)))

	// Deliverables are listed and downloaded with read access; producing them is a delivery
	// gesture, hence `release:export`.
	mux.Handle("GET /projects/{project}/releases/{tag}/exports", Chain(http.HandlerFunc(releases.Exports),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))
	mux.Handle("GET /projects/{project}/releases/{tag}/exports/{artifact}/download", Chain(http.HandlerFunc(releases.Download),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))
	mux.Handle("POST /projects/{project}/releases/{tag}/exports", Chain(http.HandlerFunc(releases.Export),
		middleware.Authenticate, middleware.CSRF, middleware.RequireIn(auth.PermReleaseExport)))

	// Test results and the score are readable by all: it is the first thing one looks at
	// before deciding anything.
	mux.Handle("GET /projects/{project}/releases/{tag}/tests", Chain(http.HandlerFunc(tests.Tests),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))
	mux.Handle("GET /projects/{project}/releases/{tag}/tests/{family}", Chain(http.HandlerFunc(tests.Family),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))
	mux.Handle("GET /projects/{project}/releases/{tag}/security", Chain(http.HandlerFunc(tests.Security),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))

	// Load campaigns are readable by all. Pinning a baseline, however, moves the reference of
	// every later verdict: that is a delivery decision, not a display preference.
	mux.Handle("GET /projects/{project}/releases/{tag}/bench", Chain(http.HandlerFunc(benches.Bench),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))
	mux.Handle("POST /projects/{project}/releases/{tag}/bench/{measure}/baseline", Chain(http.HandlerFunc(benches.PinBaseline),
		middleware.Authenticate, middleware.CSRF, middleware.RequireIn(auth.PermReleaseFinal)))

	// Administration requires `admin`, and nothing less: granting a role is what allows
	// granting others, hence the only permission that propagates. The audit log is attached
	// to it because it serves first to check those grants.
	mux.Handle("GET /admin/roles", Chain(http.HandlerFunc(admin.Roles),
		middleware.Authenticate, middleware.Require(auth.PermAdmin)))
	mux.Handle("POST /admin/roles", Chain(http.HandlerFunc(admin.Grant),
		middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermAdmin)))
	mux.Handle("DELETE /admin/roles/{subject}", Chain(http.HandlerFunc(admin.Revoke),
		middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermAdmin)))
	mux.Handle("GET /admin/audit", Chain(http.HandlerFunc(admin.Audit),
		middleware.Authenticate, middleware.Require(auth.PermAdmin)))

	// Plugin settings require `admin`, at both scopes. They carry API keys — sealed, but whose
	// writing commits the dashboard's outgoing calls — and decide whole mechanisms: disabling
	// attestation changes what the environments screen asserts.
	//
	// The project scope is administered too, and not delegated to the project's role:
	// RequireIn would grant a tracker's settings to someone who only has delivery on that
	// project, which is not the same responsibility.
	mux.Handle("GET /admin/plugins", Chain(http.HandlerFunc(plugins.List),
		middleware.Authenticate, middleware.Require(auth.PermAdmin)))
	mux.Handle("PUT /admin/plugins/{plugin}", Chain(http.HandlerFunc(plugins.Set),
		middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermAdmin)))

	mux.Handle("GET /projects/{project}/plugins", Chain(http.HandlerFunc(plugins.ProjectList),
		middleware.Authenticate, middleware.Require(auth.PermAdmin)))
	mux.Handle("PUT /projects/{project}/plugins/{plugin}", Chain(http.HandlerFunc(plugins.SetProject),
		middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermAdmin)))
	// Removal returns the project to inheriting the instance's. Distinct from disabling,
	// hence a distinct verb: "nothing particular to say" is not "no".
	mux.Handle("DELETE /projects/{project}/plugins/{plugin}", Chain(http.HandlerFunc(plugins.ClearProject),
		middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermAdmin)))

	// The registry kinds a project keeps. Administration: disabling a verification changes
	// what a release will have gone through before being shipped.
	mux.Handle("GET /projects/{project}/pipelines", Chain(http.HandlerFunc(projectPipelines.List),
		middleware.Authenticate, middleware.Require(auth.PermAdmin)))
	mux.Handle("PUT /projects/{project}/pipelines/{kind}", Chain(http.HandlerFunc(projectPipelines.Set),
		middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermAdmin)))

	// Versions are read without privilege: a version's scope is the most consulted piece of
	// information, and the first one comes looking for before deciding anything.
	mux.Handle("GET /projects/{project}/versions", Chain(http.HandlerFunc(versions.List),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))
	// Declaring a version by hand: the only entry point for a project with no tracker, whose
	// versions come from no outside tool. POST and a CSRF check, like every write.
	mux.Handle("POST /projects/{project}/versions", Chain(http.HandlerFunc(versions.Create),
		middleware.Authenticate, middleware.Require(auth.PermReleaseCreate), middleware.CSRF))

	mux.Handle("GET /projects/{project}/versions/{id}", Chain(http.HandlerFunc(versions.Get),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))

	// The repository's references serve the release creation form. Readable by all: they are
	// branch names, not secrets, and the token that reads them never leaves the server.
	//
	// Driven repositories are read with the other settings, and written like them: declaring
	// a repository says what the dashboard will clone, which is not a routine operational
	// decision.
	mux.Handle("GET /projects", Chain(http.HandlerFunc(projects.List),
		middleware.Authenticate, middleware.Require(auth.PermAdmin)))
	mux.Handle("POST /projects", Chain(http.HandlerFunc(projects.Create),
		middleware.Authenticate, middleware.Require(auth.PermAdmin), middleware.CSRF))
	mux.Handle("PUT /projects/{id}", Chain(http.HandlerFunc(projects.Update),
		middleware.Authenticate, middleware.Require(auth.PermAdmin), middleware.CSRF))
	mux.Handle("DELETE /projects/{id}", Chain(http.HandlerFunc(projects.Delete),
		middleware.Authenticate, middleware.Require(auth.PermAdmin), middleware.CSRF))
	// The check leaves the dashboard: it calls the forge. POST and a CSRF check, although it
	// only writes its own verdict.
	mux.Handle("POST /projects/{id}/probe", Chain(http.HandlerFunc(projects.Probe),
		middleware.Authenticate, middleware.Require(auth.PermAdmin), middleware.CSRF))

	// Rotating a deploy key is an outgoing action: the old one becomes void at that instant,
	// and the repository stops being clonable until the new one is pasted into it. POST and a
	// CSRF check.
	mux.Handle("POST /projects/{id}/deploy-key", Chain(http.HandlerFunc(projects.RotateDeployKey),
		middleware.Authenticate, middleware.Require(auth.PermAdmin), middleware.CSRF))

	// Activating a key no longer has a route: it used to be declared by hand, and it is now
	// observed by the access check, which reads the repository's deploy keys. An intention
	// could stay true after the key had been removed from the forge; an observation cannot.

	// A project's fact feed. Read with the ordinary read permission: a fact says what
	// happened to the project — a build concluded, a promotion succeeded — and knowing that
	// requires no privilege.
	//
	// That will stop being true the day plugin facts carry a third-party forge's payload: a
	// pull request's title says more than a version number. The permission will need
	// revisiting then, with the real case in view rather than as a precaution here.
	mux.Handle("GET /projects/{project}/events", Chain(http.HandlerFunc(events.List),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))

	// Plan runs, with ordinary read access.
	//
	// It is the same question as "why did my tests not start", and the verification chain now
	// goes through here: reserving it to administration would charge a privilege for an
	// ordinary diagnosis.
	//
	// The detail of a step — the excerpt of a third party's answer — is hidden from
	// non-administrators by the handler, which says so rather than staying silent.
	mux.Handle("GET /projects/{project}/plan-runs", Chain(http.HandlerFunc(planRuns.List),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))

	// Declared reactions. `admin`, like the plugins and for the same reason: declaring a
	// reaction makes calls go out in the instance's name and launches pipelines. That is not
	// the same responsibility as conducting a delivery, and `release:edit` would grant it to
	// someone who does not have it.
	mux.Handle("GET /projects/{project}/reactions", Chain(http.HandlerFunc(reactions.List),
		middleware.Authenticate, middleware.Require(auth.PermAdmin)))
	mux.Handle("POST /projects/{project}/reactions", Chain(http.HandlerFunc(reactions.Save),
		middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermAdmin)))
	mux.Handle("PUT /projects/{project}/reactions/{reaction}", Chain(http.HandlerFunc(reactions.Save),
		middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermAdmin)))
	mux.Handle("DELETE /projects/{project}/reactions/{reaction}", Chain(http.HandlerFunc(reactions.Delete),
		middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermAdmin)))

	// A release's automation: what the project builds, then verifies. `admin` for the same
	// reason, and one more — emptying this declaration makes a project stop building, which
	// is not repaired by rewriting it: the releases created meanwhile left with nothing.
	//
	// No DELETE: emptying the automation is a PUT of a plan with no stage. Deleting the row
	// would have it recomposed from the targets at the next restart, and "this project no
	// longer builds" would become "this project builds its three images" with nobody having
	// asked.
	mux.Handle("GET /projects/{project}/automation", Chain(http.HandlerFunc(reactions.Automation),
		middleware.Authenticate, middleware.Require(auth.PermAdmin)))
	mux.Handle("PUT /projects/{project}/automation", Chain(http.HandlerFunc(reactions.SaveAutomation),
		middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermAdmin)))

	// What this version can do: the facts that can be listened for, the steps available, the
	// conditions. The editor hard-codes no list — a list written in the interface would
	// diverge from the binary at the first addition, and the gap would show as a step one can
	// choose and nothing executes.
	mux.Handle("GET /admin/automations/catalogue", Chain(http.HandlerFunc(reactions.Catalogue),
		middleware.Authenticate, middleware.Require(auth.PermAdmin)))

	mux.Handle("GET /forge/refs", Chain(http.HandlerFunc(forge.Refs),
		middleware.Authenticate, middleware.Require(auth.PermRead)))

	// The state of the synchronisations requires `admin`: it carries error messages from the
	// deployment's tracker instance, and those say something about its infrastructure.
	//
	// Changing a status mapping moves a completion rate displayed to the client, and
	// relaunching a full synchronisation calls a third-party service: two gestures that
	// belong to administration, not to consultation.
	//
	// Under the project, since the tracker is a per-project plugin. Everything these routes
	// carry belongs to it — its cursor, its mapping, its ambiguous versions — and the mapped
	// vocabulary belongs to the deployment, not to the instance.
	mux.Handle("GET /projects/{project}/sync", Chain(http.HandlerFunc(syncAdmin.State),
		middleware.Authenticate, middleware.Require(auth.PermAdmin)))
	mux.Handle("POST /projects/{project}/sync/redmine/resync", Chain(http.HandlerFunc(syncAdmin.Resync),
		middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermAdmin)))
	mux.Handle("PUT /projects/{project}/sync/redmine/mapping/{status}", Chain(http.HandlerFunc(syncAdmin.SetMapping),
		middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermAdmin)))

	// The plugins' routes: promotion and environments for the MSE module. Each carries its
	// own access control, and the core does not know them.
	for _, mount := range mounts {
		mount(mux, middleware)
	}

	return mux
}
