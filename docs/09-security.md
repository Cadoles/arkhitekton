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

Two narrow roles, and the shape of each is the point.

**Leader lease**, in the dashboard's own namespace: `get`, `create`, `update` on
`coordination.k8s.io/leases`. Nothing else, and nowhere else.

**Tekton namespace:**

| Resource | Verbs |
|---|---|
| `pipelines`, `tasks` | get, list, watch, create, update, delete |
| `pipelineruns` | get, list, watch, create, patch, delete |
| `taskruns` | get, list, watch |
| `pods`, `pods/log` | get, list, watch |
| `secrets` | **create, patch, update, delete — not get, not list** |
| `serviceaccounts` | get, update, patch |

`delete` on definitions withdraws those of an uninstalled plugin (doc 06 §6.12): only what
carries both the dashboard's mark (`app.kubernetes.io/managed-by=arkhitekton`) and a plugin's
label (`arkhitekton/plugin`) is deleted. A definition someone else maintains is never adopted,
so never deleted either.

The secrets rule is deliberate and worth stating plainly: **the dashboard designates
secrets, it never reads them.** It can create the secret a pipeline will mount and delete
it afterwards, and it cannot read that secret's contents — nor any other secret in the
namespace.

A plugin that reads the cluster needs roles its operator grants the worker's account, and
documents them.

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

## 9.8 Inbound webhooks

`POST /api/v1/hooks/{plugin}/projects/{project}` is reachable without a session, by design: a
forge holds none. What stands in for authentication:

- **The signature is checked by the dashboard, not by the plugin.** The plugin declares the scheme
  (HMAC-SHA256 of the body, or a token in a header) and the header; the dashboard compares in
  constant time against the secret the project's plugin settings hold, sealed at rest, before the
  plugin reads a byte. A plugin cannot forget the check.
- **No secret, no delivery.** A project whose plugin has no secret refuses every delivery with
  `403`, rather than accepting unsigned ones.
- **The body is bounded** to 4 MiB before the signature is computed, so an unauthenticated caller
  cannot make the API read without limit.
- **A replay writes nothing.** A fact is keyed on what it says, a push on its repository, ref and
  commit, not on the delivery: a delivery replayed by anyone holding a copy is the same fact, not
  recorded again and not routed again. The token scheme is weaker, since a token read in transit
  signs any body; prefer HMAC where the forge offers it.
- **The repository must be the project's.** A secret shared at the instance's scope signs every
  repository's hook alike; the Gitea plugin refuses a delivery about another repository than the
  project's, so a hook pointed at the wrong address cannot build the wrong code.
- **A delivery launches nothing itself.** It records facts; what runs is what the project's
  reactions, declared by an administrator, say.

What it costs an unauthenticated caller to try: a project lookup, a settings read and an HMAC
over at most 4 MiB. Rate limiting, if wanted, belongs to the ingress.

## 9.9 Files from pipelines

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

## 9.10 Plugin code

A plugin compiled into the binary is trusted like the rest of it. A **declared reading** is not
code: it only projects fields through a closed set of filters, with no loop, condition or call,
and is checked when the plugin is loaded. Whatever reads a run, the dashboard checks the facts
returned, stamps the producer itself, and refuses an attachment naming an object the run did not
keep.

**Installed plugins** (doc 06 §6.12) are trusted by the administrator who installs them, as in
Jenkins or Grafana: installing is an administrative act, and a plugin's reliability is theirs to
judge. A plugin's **program** (doc 06 §6.4) runs as the dashboard's user, in its pods; its
**interface** runs in the page, with the user's session. There is no sandbox, and the dashboard
does not pretend otherwise. What it guarantees:

- **supply chain**: installed by digest only, never by tag; signed in cosign's simple signing
  format by a key the operator mounted (`PLUGIN_TRUSTED_KEYS`), verified before the layer is
  fetched, with no call to sigstore's services; the signed payload must name the digest
  installed. A plugin unsigned or signed by another key installs only if the operator names it
  in `PLUGIN_ALLOW_UNSIGNED`, and is recorded `unsigned`. The program's digest is checked again
  before each launch; the key's fingerprint and the digest are recorded and audited;
- **the cluster stays the operator's**: no service account, from the manifest or from a binding
  recorded on the screen; a pipeline may ask for `cluster-deploy`, which only the operator's
  catalogue file binds; Tekton definitions limited to Pipelines and Tasks, with no host path, host namespace, privileged container,
  resolver or bundle. A program reaches the cluster with the worker's service account, whose
  RBAC is the real bound: it still cannot read a Secret (§9.4);
- **routes**: authentication, CSRF, the plugin's switch for the project and the declared
  permission are checked by the dashboard before a program sees a request; the program never
  receives the session;
- **environment**: a program is given its database connection and, in the worker, the cluster's
  address, not the sealing key, the object store's or OIDC's secrets, nor forge and tracker
  tokens. Hygiene rather than a wall: a process of the same user in the same pod could read its
  parent's;
- **credentials**: registry credentials are a Docker `config.json` mounted in the worker, read
  for its `auths` only. They never reach the database. A program bringing a tracker, a forge,
  probes or actions is handed its own settings for each call, unsealed, over the local mTLS
  connection: they are the plugin's, as they are a compiled plugin's;
- **purge**: undoing an uninstalled plugin's tables runs its own `Down` migrations, with the trust
  its `Up` migrations had; an administrator asks for it, typing the plugin's id, and it is
  audited.

What the definitions' pods may do is still bounded by the namespace's Pod Security level: the
dashboard's checks refuse the obvious escapes, the cluster enforces the rest.

## 9.11 Audit

Every administrative act (role granted or revoked, plugin enabled, installed or refused, activated or
uninstalled, its tables purged, capability bound, limit changed, project created) is recorded in `audit_log` with
its actor, target and payload, and is readable at `GET /admin/audit` by an administrator.
