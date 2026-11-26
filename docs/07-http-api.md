# 7. HTTP API

## 7.1 Conventions

**Base path.** Application routes are served under `/api/v1`. Everything else is the web
interface. `/healthz` and `/readyz` sit outside the prefix.

**Content type.** JSON in, JSON out. Errors are `application/problem+json` (RFC 9457), with a
`title`, a `status` and a `detail`:

```json
{ "title": "Unknown kind", "status": 400, "detail": "unit-test" }
```

Titles and details are human-readable English strings: the interface displays them as they
arrive. `401` means no session or an expired one; `403`, no role granted, a missing permission or
a missing CSRF token.

**Authentication.** OIDC authorization code flow. A session cookie (`mstore_session`,
HttpOnly, SameSite=Lax) carries the session; a second cookie (`mstore_csrf`) carries a token
that every unsafe method must echo in `X-CSRF-Token`. SameSite covers cross-site
navigation, not a cross-site request issued by a script, which is what the token is for.

**Authorization.** Two shapes, and the difference matters:

- `Require(perm)` — an instance-wide permission.
- `RequireIn(perm)` — the permission **in the project named by the path**. A user may be
  an operator on one project and a viewer on another.

Permissions and roles are described in [09 — Security](09-security.md).

**Lists.** There is no cursor. Lists take an optional `limit` (`/runs` and `/admin/journal`
default to 100), a log is read by `offset` and `length`, a group's test cases by `offset` and
`q`. `?download=1` serves a log or an attachment as a file.

**Streaming.** `GET /runs/events` (event `run`) and `GET /runs/{run}/tasks/{task}/log/stream`
(events `log` and `sealed`, the id being the byte offset) are Server-Sent Events, resumed from
`Last-Event-ID`; a failure ends the stream with an `error` event. They are exempt from
`HTTP_WRITE_TIMEOUT`: each write has 30 seconds of its own, which lets a client that stopped
reading go, and a comment every 20 seconds keeps an idle stream open.

## 7.2 Session

| Route | Permission | Purpose |
|---|---|---|
| `GET /auth/login` | public | Starts the OIDC flow |
| `GET /auth/callback` | public | Completes it, creates the session |
| `POST /auth/logout` | CSRF | Revokes the session |
| `GET /me` | authenticated | Identity and instance role, the projects (slug, label, modules switched on), the active modules and the plugins' interfaces |
| `GET /ui?project={slug}` | authenticated | The screens, menus and widgets of the core and of the plugins switched on for the project, filtered for the caller's role (doc 06 §6.14); `ETag`, answers 304 to `If-None-Match` |

## 7.3 Runs and logs

| Route | Permission |
|---|---|
| `GET /runs` | `read` |
| `GET /runs/events` | `read` (SSE) |
| `GET /runs/{run}` | `read` |
| `GET /runs/{run}/tasks/{task}/log` | `read` |
| `GET /runs/{run}/tasks/{task}/log/stream` | `read` (SSE) |
| `GET /runs/{run}/tasks/{task}/log.txt` | `read` (plain text) |

## 7.4 Queue

| Route | Permission |
|---|---|
| `GET /queue` | `read` |
| `POST /queue` | `pipeline:run` |
| `POST /queue/{id}/cancel` | `pipeline:cancel` |
| `PUT /queue/limits/{kind}` | `admin` |
| `DELETE /queue/limits/{kind}` | `admin` |

`GET /queue` returns the `queues`, one per kind with its ceiling and how many of its jobs run and
wait, and the active `jobs` of every queue in the order of service: running, then waiting. A job
says what it is about with its `project`, `release_tag` or `pull_request`, and why it waits with
its `queue_reason`.

## 7.5 Releases

All scoped to a project; the permission is checked **in that project**.

| Route | Permission |
|---|---|
| `GET /projects/{project}/releases` | `read` |
| `GET /projects/{project}/releases/{tag}` | `read` |
| `POST /projects/{project}/releases` | `release:create` |
| `PUT /projects/{project}/releases/{tag}/draft` | `release:create` |
| `POST /projects/{project}/releases/{tag}/launch` | `release:create` |
| `DELETE /projects/{project}/releases/{tag}` | `release:create` |
| `POST /projects/{project}/releases/{tag}/build` | `release:create` |
| `POST /projects/{project}/releases/{tag}/verify/{kind}` | `release:create` |
| `POST /projects/{project}/releases/{tag}/run` | `pipeline:run` |
| `POST /projects/{project}/releases/{tag}/release` | `release:create:final` |
| `GET /projects/{project}/releases/{tag}/notes` | authenticated |
| `POST /projects/{project}/releases/{tag}/notes/{issue}` | `release:edit` |
| `DELETE /projects/{project}/releases/{tag}/notes/{issue}` | `release:edit` |
| `POST /projects/{project}/releases/{tag}/notes/detect` | `release:edit` |
| `PUT /projects/{project}/releases/{tag}/actions` | `release:edit` |

Creating a release creates a draft (`201`, state `draft`): its note is detected, and nothing
else leaves. `launch` resolves its reference, sets its git tag and opens its plan; a release
whose kind is `from-source`, a final, opens a plan of what its source built instead: one re-tag
per image the source published and promotes, and the build again of what does not re-tag, from
the source's commit under the release's tag. Its git tag goes on that same commit. A tag is never
moved: one the forge already carries on another commit refuses the launch (`409`). The release is `building` until that plan's build
stage concludes, then `ready`, or `failed`, which `build` relaunches. A project that declares no
automation releases a tag, `ready` at once. A second launch answers `409`, and so does a launch
whose tag the forge already carries on another commit (the draft stays a draft, to be given
another tag). `draft` changes the git reference of a
draft that builds (`{"git_ref": "…"}`), `DELETE` removes a draft, and `notes/detect` detects
its note again; each answers `409` once the release is launched. Launching or deleting also
requires the permission the release's kind declares, as creating it does (a final:
`release:create:final`).

`run` queues any pipeline `{kind, params}` on a launched release: the revision built, the
release's tag and the images built under it are filled and cannot be overridden, and a missing
input is refused with its name (`400`); a draft answers `409`. `verify/{kind}` re-runs a
verification with nothing but what the release fills, and stays for compatibility.

A release's deliverables are a plugin's routes, under `/projects/{project}/plugins/{plugin}/…`:
the Mstore plugin's delivery note is produced with `release:export`, the permission a plugin route
names for it.

## 7.6 Results

| Route | Permission |
|---|---|
| `GET /projects/{project}/releases/{tag}/tests` | `read` |
| `GET /projects/{project}/releases/{tag}/tests/{group}` | `read` |
| `GET /projects/{project}/releases/{tag}/attachments/{id}` | `read` |
| `GET /projects/{project}/releases/{tag}/security` | `read` |
| `GET /projects/{project}/releases/{tag}/bench` | `read` |
| `POST /projects/{project}/releases/{tag}/bench/{measure}/baseline` | `release:create:final` |

**Pull requests.** The same results, read for a pull request: `GET /projects/{project}/pulls/{number}/tests`,
`…/tests/{group}`, `…/attachments/{id}` and `…/security`, all `read`. A pull request also has
`…/review`, `read`: its latest result of the `review` group, with its findings, its attributes
(`verdict`, `head`, `reviewers`, `questions`, `failed`, `models`) and its files, or
`{"reviewed": false}`. The security routes read the `security` group only, and `?capability=` reads
one pipeline's audit, as a tile opens it (a release's route takes it too). A group is compared with
the previous report on the same request (its previous push) rather than with a previous release.
The score is a release's: one tile per pipeline launched (doc 05 §5.8).

| Route | Permission | Purpose |
|---|---|---|
| `GET /projects/{project}/pulls?state=open\|closed` | `read` | The project's pull requests, as the worker follows them; `closed` includes the merged ones |
| `GET /projects/{project}/pulls/{number}` | `read` | A pull request, the jobs launched on it and the plans its facts opened |
| `POST /projects/{project}/pulls/{number}/launch` | `pipeline:run` | Queues `{kind, params}` on the head: the revision, the request's tag `pr-<n>-<commit>` and the images built under it are filled and cannot be overridden |

**Statistics.** `GET /stats`, signed in, counts the releases launched and the pipelines finished
over a period: `range` is `30d` or `90d`, cut by week, or `12m`, cut by month (default `30d`). With
`project` (a slug), that project, `403` unless the caller is an operator in it; without, every
project the caller is an operator in (`pipeline:run`). The answer carries `range`, `bucket` (`week`
or `month`), `since` (the first bucket's start, a Monday or the first of a month, UTC), the
`projects` counted, `releases` (`total`, `by_state` and `by_kind` as `{key, label, count}`, and
`buckets`, every week or month of the period with its `count` and `by_state`, empty ones included)
and `pipelines`, one per kind (`kind`, the catalogue's `label`, `runs`, `succeeded`, `failed`,
`cancelled`, `median_seconds` of the runs that started). A draft is not counted, nor a job attached
to no project, nor a core task (`release-notes`): a pipeline's kind names its plugin. Plugins bring
no indicator of their own: what they record stays on their screens.

## 7.7 Versions and issues

| Route | Permission |
|---|---|
| `GET /projects/{project}/versions` | `read` |
| `POST /projects/{project}/versions` | `release:create` |
| `GET /projects/{project}/versions/{id}` | `read` |
| `GET /projects/{project}/versions/{id}/suggested-tags` | `read` |

A version's `suggested_tags` propose one tag per release kind, from the dashboard's releases of
that version: `X.Y.Z-rc.<N+1>` for a kind that requires a candidate, `X.Y.Z` for the others.
`suggested-tags` counts the repository's tags as well, read from the forge, and says whether it
could (`forge_read`): the creation form asks it when it opens, so that it does not propose a tag
the repository already has.

## 7.8 Events, plans and automation

What an automation did is read in the journal (`GET /admin/journal`, below), under the event that
set it off, steps included; a pull request's page shows its own.

| Route | Permission |
|---|---|
| `GET /projects/{project}/reactions` | `admin` |
| `POST /projects/{project}/reactions` | `admin` |
| `PUT /projects/{project}/reactions/{reaction}` | `admin` |
| `DELETE /projects/{project}/reactions/{reaction}` | `admin` |
| `PUT /projects/{project}/reactions/{reaction}/enabled` | `admin` |
| `GET /projects/{project}/automation` | `admin` |
| `PUT /projects/{project}/automation` | `admin` |
| `PUT /projects/{project}/automation/enabled` | `admin` |
| `GET /admin/automations/catalogue` | `admin` |
| `GET /projects/{project}/schedules` | `admin` |
| `POST /projects/{project}/schedules` | `admin` |
| `PUT /projects/{project}/schedules/{schedule}` | `admin` |
| `DELETE /projects/{project}/schedules/{schedule}` | `admin` |
| `PUT /projects/{project}/schedules/{schedule}/enabled` | `admin` |
| `POST /hooks/{plugin}/projects/{project}` | none: the delivery's signature, see below |

**Receiving a webhook.** The one route that changes something without a session. The plugin
declares how a delivery is signed (doc 06 §6.5); the dashboard checks it against the secret the
project's plugin settings hold, before the plugin reads the body. `202` with `{"facts", "new"}`
once the facts are recorded, including when the delivery is about nothing the plugin observes;
`401` for a signature that does not match; `403` when no secret is set, since an unsigned delivery
is never accepted; `404` for an unknown plugin or project, or a plugin switched off for the
project; `413` above 4 MiB; `422` with the plugin's reason when it cannot read an authenticated
delivery. A delivery launches nothing itself: the reactions of the project do.

**A release's automation** reads as `{plan, declared, enabled, updated_by}`. `declared` is false
while no automation was ever recorded: the worker's catch-up will compose a default one, an image
named after the project when its `docker` settings name a Dockerfile, then its verifications. A
build step may carry `promote: false` (refused outside a `build` stage): a final does not re-tag what
it publishes. Switched off (`enabled: false`), it keeps its plan and a declared release launches no
pipeline. `PUT /automation` replaces the plan and leaves the switch where it is.

**Switching an automation** takes `{"enabled": true|false}` on the `…/enabled` routes; a body
without the key is a `400`. Only the switch is written, without validating the plan again, so a
reaction whose fact is no longer produced can still be switched off. `404` for an unknown reaction
or schedule, `409` for a release automation never declared: recording one to switch it would stop
the catch-up. A schedule switched on gets its next firing from now, returned as `next_fire_at`.
Each switch is audited as `reaction.enable`, `schedule.disable`, `automation.enable` and so on.

**A schedule** is `{name, cron, timezone, ref, enabled, plan}`: five cron fields (`*`, values,
ranges, lists, steps, English month and day abbreviations, `@daily` and its siblings) read in an
IANA zone, empty meaning UTC; `ref` a branch or tag resolved to its commit at each tick through
the project's forge plugin, empty meaning the default branch. Saving returns `next_fire_at`,
computed from now: a schedule switched back on does not fire the ticks it slept through. The list
adds `next_fire_at`, `last_fired_at` and `last_outcome`. A stage of a schedule's plan carries no
role, for the reason a reaction's does not: it aims at no release.

## 7.9 Projects and administration

| Route | Permission |
|---|---|
| `GET /projects` | `admin` |
| `POST /projects` | `admin` |
| `PUT /projects/{id}` | `admin` |
| `DELETE /projects/{id}` | `admin` |
| `POST /projects/{id}/probe` | `admin` |
| `POST /projects/{id}/deploy-key` | `admin` |
| `GET /projects/{project}/pipelines` | `admin` |
| `PUT /projects/{project}/pipelines/{kind}` | `admin` |
| `GET /projects/{project}/sync` | `admin` |
| `POST /projects/{project}/sync/tracker/resync` | `admin` |
| `PUT /projects/{project}/sync/tracker/mapping/{status}` | `admin` |
| `GET /admin/roles`: what each role allows (`permissions`, and `deploy.open` / `deploy.sensitive` for promoting) · `POST /admin/roles` (`{subject, username?, role, project?}`, a person's own role; closes their sessions) · `DELETE /admin/roles/{subject}?project=` | `admin` |
| `GET /admin/people`: everyone who signed in or holds a role, those with no access first (`pending`), each with their own `roles`, their `groups`, and what applies (`global`, `projects`, each with the group it comes from as `source`) | `admin` |
| `GET /admin/groups`: the groups given a role, and the groups seen at sign-in (`known`, with their number of accounts) · `PUT /admin/groups/{group}/roles` (`{role, project?}`) · `DELETE /admin/groups/{group}/roles?project=` | `admin` |
| `GET /admin/journal`: what happened in the application, newest first. `nature` (`action`, `event`, or `automation`: the events that set off a run and the runs no event opened), `project` (a slug), `q` (searched in an action's name and target, an event's type and subject, a run's trigger and name) and `limit` (500 at most) narrow it. An action carries `actor`, `action`, `target`, `payload` and its `project` when it was done on a project's route; an event carries `type`, `source`, `subject`, `payload`, `project`, `recorded_at` when it differs from `at`, `pending` before it is routed, and the `automations` it set off. A run no event opened, a schedule's tick, is a line of nature `automation` carrying its one run. A run carries `id`, `trigger`, the reaction's or schedule's `name`, `event_key`, `state`, `created_at`, `context` and its `stages`, each with its `steps` (`kind`, `ref`, `label`, `state`, `job_id`, `detail`) | `admin` |
| `GET /admin/plugins` · `PUT /admin/plugins/{plugin}` | `admin` |
| `GET /admin/capabilities` · `PUT /admin/capabilities/{plugin}/{name}/binding` (an entry of doc 05 §5.2, YAML or JSON; `400` when invalid or naming a service account, `404` for an unknown capability, `409` when the catalogue file binds it) · `DELETE …/binding` · `PUT …/weight` (`{"weight": 0 to 100}`, zero out of the score, file-bound capabilities included) · `DELETE …/weight` (back to its nature's); the list carries each capability's `weight`, `default_weight` and `group` | `admin` |
| `GET /admin/plugins/installations` (the installations, and under `plugins` each plugin's source: repository, pinned, latest version found, `update_available`, `major`) · `POST /admin/plugins/installations` (`{"reference": "registry/repository"}`, followed, or with a tag or `@sha256:…`, pinned; queues the job, `202`; `409` when a plugin is installed from that repository, unless `"mode"` is `"install"`, beside it, or `"update"`, in its place) | `admin` |
| `POST /admin/plugins/{plugin}/update` (installs the newer version found in place of the active one, `202`; `409` when the plugin is pinned or up to date) · `POST /admin/plugins/{plugin}/check` (checks its repository within seconds, `202`) · `PUT /admin/plugins/{plugin}/source` (`{"pinned": true\|false}`) | `admin` |
| `POST /admin/plugins/installations/{id}/activate` (`409` when the capabilities would not build with it) | `admin` |
| `POST /admin/plugins/installations/{id}/accept` (accepts an unrecognised source and queues the installation again; `409` unless it is waiting on that decision) | `admin` |
| `DELETE /admin/plugins/installations/{id}` (the active version uninstalls the plugin) | `admin` |
| `GET /admin/plugins/leftovers` (the tables uninstalled plugins left) · `POST /admin/plugins/leftovers/{plugin}/purge` (queues the purge, `202`; `404` when the plugin left none, `409` while an installation is pending) | `admin` |
| `GET /projects/{project}/plugins` | `admin` |
| `PUT /projects/{project}/plugins/{plugin}` | `admin` |
| `DELETE /projects/{project}/plugins/{plugin}` | `admin` — clears the override, restoring inheritance |
| `GET /projects/{project}/forge/refs` | `read` |

## 7.10 Routes contributed by plugins

An installed plugin's program serves the routes its manifest declares (doc 06 §6.12):

| Route | Permission |
|---|---|
| `/projects/{project}/plugins/{plugin}/…` | the route's, checked by the dashboard; `404` when the project switches the plugin off or the route is not declared |
| `/plugins/{plugin}/…` | the route's, on the instance |
| `GET /plugin-interfaces/{plugin}/{version}/…` | signed in: the active version's interface files, cached for good |

`503` says the program is restarting, `504` that it did not answer in 30 seconds, `502` that it
failed; its error is logged, never returned. `403` names the missing permission, `413` refuses a
body above 4 MiB. The plugin documents its routes.

## 7.11 Outside the API prefix

| Route | Purpose |
|---|---|
| `GET /healthz` | The process is alive |
| `GET /readyz` | Every dependency answers; see [10 §10.3](10-operations.md#103-health-and-readiness) |
| `POST /mcp` | The MCP server, for AI agents (§7.12) |
| `GET /.well-known/oauth-protected-resource/mcp` | The MCP server's protected resource metadata (RFC 9728) |
| `GET /.well-known/oauth-authorization-server` | The authorisation server's metadata (RFC 8414) |
| `POST /oauth/register` · `GET /oauth/authorize` · `POST /oauth/token` · `POST /oauth/revoke` | Registration (RFC 7591), authorisation, tokens and revocation (RFC 7009) of AI agents; doc 09 §9.12 |
| `GET /` | The web interface |

## 7.12 AI agents

`/mcp` is an MCP server over streamable HTTP, stateless, answering in JSON. It takes a bearer
token from `/oauth/token`; without one, or with an invalid one, `401` and a `WWW-Authenticate`
header naming the resource metadata, from which a client finds where to authorise. Each tool calls
a route of the API in memory, as the person who authorised the agent: a refusal is the route's,
returned as a tool error with its status and title (`403 Insufficient permission`).

| Tools | Scope | Route |
|---|---|---|
| `whoami` | `dashboard:read` | `GET /me` |
| `list_runs`, `get_run`, `read_task_log` (the end of the log without `offset`) | `dashboard:read` | `GET /runs…` |
| `get_queue` (the jobs, and the pipeline kinds with their inputs) | `dashboard:read` | `GET /queue` |
| `list_releases`, `get_release`, `get_release_notes`, `get_test_results`, `get_security_report`, `get_bench` | `dashboard:read` | `GET /projects/{project}/releases…` |
| `list_pull_requests`, `get_pull_request` | `dashboard:read` | `GET /projects/{project}/pulls…` |
| `list_versions`, `get_version`, `list_forge_refs` | `dashboard:read` | `GET /projects/{project}/versions…`, `…/forge/refs` |
| `get_statistics` | `dashboard:read` | `GET /stats` |
| `read_journal` | `dashboard:read` | `GET /admin/journal` (administrators) |
| `launch_pipeline`, `cancel_job` | `dashboard:run` | `POST /queue`, `POST /queue/{id}/cancel` |
| `create_release`, `launch_release`, `rebuild_release`, `run_release_pipeline`, `approve_release` | `dashboard:run` | `POST /projects/{project}/releases…` |
| `launch_pull_request_pipeline` | `dashboard:run` | `POST /projects/{project}/pulls/{number}/launch` |

A token without `dashboard:run` is not offered the tools that act. An answer above 256 KiB is cut,
and says so.

In the API, signed in:

| Route | Permission | Purpose |
|---|---|---|
| `GET /oauth/requests/{id}` | authenticated | A request waiting for consent: `client`, `redirect_host`, `scopes`; `404` when unknown, expired or answered |
| `POST /oauth/requests/{id}/approve` · `POST /oauth/requests/{id}/deny` | CSRF | Approves (`{scopes}`, those kept among those asked; reading always) or refuses; answers `{redirect}`, where to send the browser |
| `GET /agents` | authenticated | The agents the caller authorised (`id`, `name`, `scopes`, `created_at`, `last_used`), everyone's with their `owner` to an administrator (`all`), and the `mcp_url` |
| `DELETE /agents/{id}` | CSRF | Revokes an agent: the caller's own, anyone's for an administrator |
