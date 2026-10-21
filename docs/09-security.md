# 9. Security and permissions

## 9.1 Authentication

OIDC authorization code flow, with PKCE. The issuer URL must match the `iss` claim
**exactly**, trailing slash included — a mismatch fails verification with a message that
does not point at the cause.

A successful callback creates a server-side session. The cookie carries an opaque id and
nothing else: `HttpOnly`, `SameSite=Lax`, `Secure` behind HTTPS. Sessions expire after
`SESSION_TTL` and are purged by the worker.

Unsafe methods require a CSRF token: a second cookie carries it, the request echoes it in
`X-CSRF-Token`. `SameSite=Lax` covers cross-site *navigation*, not a cross-site request
issued by a script — which is exactly what the token is for.

## 9.2 Roles

Four roles, ordered. Each includes what the ones before it can do:

| Role | Adds |
|---|---|
| `viewer` | Read everything it may see; export a release |
| `operator` | Run and cancel pipelines; create and edit releases |
| `releaser` | Make a release final; pin a benchmark baseline |
| `admin` | Everything, including administration |

Permissions are named strings — `read`, `pipeline:run`, `pipeline:cancel`,
`release:create`, `release:create:final`, `release:edit`, `release:export`, `admin` — and
one family is computed: `deploy:<environment>`.

**Roles are per project, with an instance-wide fallback.** An identity carries a global
role and a map of project roles; a route scoped to a project resolves the role *in that
project*. Someone may be an operator on one project and a viewer on another.

## 9.3 Deployment permissions

Promoting to an environment requires `deploy:<environment>` **in the project that owns
it**. Whether that permission is granted to an operator or reserved to a releaser depends
on the environment's `sensitive` flag, declared per project in the catalogue.

The resolution **fails closed**: an environment that the catalogue does not declare is
treated as sensitive, so an unknown environment demands the higher role rather than the
lower. A typo in a promotion target cannot widen access.

## 9.4 Kubernetes RBAC

Three narrow roles, and the shape of each is the point.

**Leader lease**, in the dashboard's own namespace: `get`, `create`, `update` on
`coordination.k8s.io/leases`. Nothing else, and nowhere else.

**Tekton namespace:**

| Resource | Verbs |
|---|---|
| `pipelines`, `tasks` | get, list, watch, create, update |
| `pipelineruns` | get, list, watch, create, patch, delete |
| `taskruns` | get, list, watch |
| `pods`, `pods/log` | get, list, watch |
| `secrets` | **create, patch, update, delete — not get, not list** |
| `serviceaccounts` | get, update, patch |

The secrets rule is deliberate and worth stating plainly: **the dashboard designates
secrets, it never reads them.** It can create the secret a pipeline will mount and delete
it afterwards, and it cannot read that secret's contents — nor any other secret in the
namespace.

**Flux namespace:** read-only on `imagepolicies`, `imagerepositories`,
`imageupdateautomations`, `kustomizations` and `gitrepositories`.

## 9.5 Pod hardening

The namespace enforces the `restricted` Pod Security Standard, so a manifest that later
drifts fails admission rather than running.

Both deployments run as non-root, with a read-only root filesystem, no privilege
escalation, all capabilities dropped and the default seccomp profile.

**The API pod runs with `automountServiceAccountToken: false`.** It has no business
talking to the API server; the worker, which does, mounts its token.

## 9.6 Credentials, and where they are not

**Registry credentials are never stored in the database.** They are referenced by the
name of a Kubernetes secret, which the pipeline mounts. The one deliberate exception is
tracker API keys, which are encrypted at rest.

**Plugin secret fields** are sealed with `SECRETS_KEY` before reaching the database and
are never returned by the API. The key lives in the environment: a database backup says
nothing without it. Rotation is two-staged — the new key seals, the retired keys still
open — because values are re-sealed only as they are rewritten.

**Deploy keys are per project.** Arkhitekton generates the pair, publishes the public
half for an administrator to install on the forge, and keeps the private half in a
Kubernetes secret that pipelines mount. Rotation is a request recorded on the project,
so that the old key keeps working until the new one is installed.

## 9.7 Outbound requests

**The tracker transport refuses any method but GET and HEAD** before the request leaves
the process. The guarantee is structural rather than a matter of caller discipline,
because the key in circulation is a production one.

**The forge transport is read-only** for browsing refs. Writing to the forge — deploy
keys, commit statuses — goes through separate clients with their own credentials.

**Webhook targets are guarded.** Private addresses, loopback and plain `http://` are
refused unless explicitly allowed, and link-local is never reopened: a URL typed in the
interface becomes a request issued from inside the cluster, which is a
server-side-request-forgery primitive if left open.

## 9.8 Files from pipelines

The files a run keeps (screenshots, videos, HTML reports) are third-party content served under
the dashboard's origin, where the session cookie lives and the CSRF token can be read by
script. Every one is served with `Content-Security-Policy: sandbox` (no scripts, an opaque
origin, nothing loaded but its own images and media), `nosniff`, and inline only for types a
browser renders without running anything of the file's: images other than SVG, MP4 and WebM
videos, text and HTML. Everything else, SVG included, is a download of opaque bytes. The
interface frames an HTML report with an empty `sandbox` attribute as well.

A file is read through the API under the reader's role on the release, never through a
presigned URL, and a file of another release answers like a missing one. An interpreter can
only attach a file the dashboard itself kept for that run.

## 9.9 Plugin code

A plugin compiled into the binary is trusted like the rest of it. A plugin's **reading** of its
runs is not code the dashboard trusts when it comes as a declaration or a WebAssembly module:

- a declaration only projects fields through a closed set of filters, with no loop, condition
  or call, and is checked when the plugin is loaded;
- a WebAssembly module runs in a fresh instance per run, with an empty WASI (no file, no
  environment, no argument, no network) and no other host function, bounded memory, duration,
  answer and console (doc 06 §6.4).

Either way the dashboard checks the facts returned, stamps the producer itself, and refuses an
attachment naming an object the run did not keep.

## 9.10 Audit

Every administrative act — role granted or revoked, plugin enabled, limit changed,
project created — is recorded in `audit_log` with its actor, target and payload, and is
readable at `GET /admin/audit` by an administrator.
