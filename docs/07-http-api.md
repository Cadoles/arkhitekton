# 7. HTTP API

## 7.1 Conventions

**Base path.** Application routes are served under `/api/v1`. Everything else is the web
interface. `/healthz` and `/readyz` sit outside the prefix.

**Content type.** JSON in, JSON out. Errors are `application/problem+json` (RFC 7807):

```json
{ "title": "Unknown run kind", "status": 400, "detail": "unit-test" }
```

Titles and details are human-readable English strings — the interface displays them as
they arrive.

**Authentication.** OIDC authorization code flow. A session cookie (`mse_session`,
HttpOnly, SameSite=Lax) carries the session; a second cookie (`mse_csrf`) carries a token
that every unsafe method must echo in `X-CSRF-Token`. SameSite covers cross-site
navigation, not a cross-site request issued by a script, which is what the token is for.

**Authorization.** Two shapes, and the difference matters:

- `Require(perm)` — an instance-wide permission.
- `RequireIn(perm)` — the permission **in the project named by the path**. A user may be
  an operator on one project and a viewer on another.

Permissions and roles are described in [09 — Security](09-security.md).

**Streaming.** Routes ending in `/events` or `/stream` are Server-Sent Events. They are
exempt from the write timeout.

## 7.2 Session

| Route | Permission | Purpose |
|---|---|---|
| `GET /auth/login` | public | Starts the OIDC flow |
| `GET /auth/callback` | public | Completes it, creates the session |
| `POST /auth/logout` | CSRF | Revokes the session |
| `GET /me` | authenticated | Identity, instance role, per-project roles |
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

## 7.5 Releases

All scoped to a project; the permission is checked **in that project**.

| Route | Permission |
|---|---|
| `GET /projects/{project}/releases` | `read` |
| `GET /projects/{project}/releases/{tag}` | `read` |
| `POST /projects/{project}/releases` | `release:create` |
| `POST /projects/{project}/releases/{tag}/build` | `release:create` |
| `POST /projects/{project}/releases/{tag}/verify/{kind}` | `release:create` |
| `POST /projects/{project}/releases/{tag}/release` | `release:create:final` |
| `GET /projects/{project}/releases/{tag}/notes` | authenticated |
| `POST /projects/{project}/releases/{tag}/notes/{issue}` | `release:edit` |
| `DELETE /projects/{project}/releases/{tag}/notes/{issue}` | `release:edit` |
| `PUT /projects/{project}/releases/{tag}/actions` | `release:edit` |
| `GET /projects/{project}/releases/{tag}/exports` | `read` |
| `POST /projects/{project}/releases/{tag}/exports` | `release:export` |
| `GET /projects/{project}/releases/{tag}/exports/{artifact}/download` | `read` |

## 7.6 Results

| Route | Permission |
|---|---|
| `GET /projects/{project}/releases/{tag}/tests` | `read` |
| `GET /projects/{project}/releases/{tag}/tests/{group}` | `read` |
| `GET /projects/{project}/releases/{tag}/attachments/{id}` | `read` |
| `GET /projects/{project}/releases/{tag}/security` | `read` |
| `GET /projects/{project}/releases/{tag}/bench` | `read` |
| `POST /projects/{project}/releases/{tag}/bench/{measure}/baseline` | `release:create:final` |

## 7.7 Versions and issues

| Route | Permission |
|---|---|
| `GET /projects/{project}/versions` | `read` |
| `POST /projects/{project}/versions` | `release:create` |
| `GET /projects/{project}/versions/{id}` | `read` |

## 7.8 Events, plans and automation

| Route | Permission |
|---|---|
| `GET /projects/{project}/events` | `read` |
| `GET /projects/{project}/plan-runs` | `read` |
| `GET /projects/{project}/reactions` | `admin` |
| `POST /projects/{project}/reactions` | `admin` |
| `PUT /projects/{project}/reactions/{reaction}` | `admin` |
| `DELETE /projects/{project}/reactions/{reaction}` | `admin` |
| `GET /projects/{project}/automation` | `admin` |
| `PUT /projects/{project}/automation` | `admin` |
| `GET /admin/automations/catalogue` | `admin` |
| `POST /hooks/{plugin}/projects/{project}` | none: the delivery's signature, see below |

**Receiving a webhook.** The one route that changes something without a session. The plugin
declares how a delivery is signed (doc 06 §6.5); the dashboard checks it against the secret the
project's plugin settings hold, before the plugin reads the body. `202` with `{"facts", "new"}`
once the facts are recorded, including when the delivery is about nothing the plugin observes;
`401` for a signature that does not match; `403` when no secret is set, since an unsigned delivery
is never accepted; `404` for an unknown plugin or project, or a plugin switched off for the
project; `413` above 4 MiB; `422` with the plugin's reason when it cannot read an authenticated
delivery. A delivery launches nothing itself: the reactions of the project do.

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
| `GET /admin/roles` · `POST /admin/roles` · `DELETE /admin/roles/{subject}` | `admin` |
| `GET /admin/audit` | `admin` |
| `GET /admin/plugins` · `PUT /admin/plugins/{plugin}` | `admin` |
| `GET /admin/capabilities` · `PUT`/`DELETE /admin/capabilities/{plugin}/{name}/binding` | `admin` |
| `GET /admin/plugins/installations` · `POST /admin/plugins/installations` (`{"reference": "repository@sha256:…"}`, queues the job, `202`) | `admin` |
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
failed; its error is logged, never returned. The plugin documents its routes.

## 7.11 Outside the API prefix

| Route | Purpose |
|---|---|
| `GET /healthz` | The process is alive |
| `GET /readyz` | Every dependency answers; see [10 §10.3](10-operations.md#103-health-and-readiness) |
| `GET /` | The web interface |
