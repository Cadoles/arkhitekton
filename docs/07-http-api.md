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
| `GET /projects/{project}/plugins` | `admin` |
| `PUT /projects/{project}/plugins/{plugin}` | `admin` |
| `DELETE /projects/{project}/plugins/{plugin}` | `admin` — clears the override, restoring inheritance |
| `GET /projects/{project}/forge/refs` | `read` |

## 7.10 Routes contributed by the MSE plugin

Mounted by the plugin, gated per project: a project with the module disabled refuses
them.

| Route | Permission |
|---|---|
| `GET /projects/{project}/environments` | `read` |
| `GET /projects/{project}/environments/{environment}` | `read` |
| `GET /projects/{project}/environments/{environment}/history` | `read` |
| `GET /projects/{project}/releases/{tag}/targets` | `read` |
| `POST /projects/{project}/releases/{tag}/promote/{environment}` | `deploy:<environment>` in the project |
| `GET /projects/{project}/releases/{tag}/signoffs` | `read` |
| `PUT /projects/{project}/releases/{tag}/signoffs/{env}` | per environment |
| `DELETE /projects/{project}/releases/{tag}/signoffs/{env}` | per environment |

## 7.11 Outside the API prefix

| Route | Purpose |
|---|---|
| `GET /healthz` | The process is alive |
| `GET /readyz` | Every dependency answers; see [10 §10.3](10-operations.md#103-health-and-readiness) |
| `GET /` | The web interface |
