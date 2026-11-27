# 4. Configuration

Everything is read from the environment, once, at start-up, apart from the pipeline catalogue
and the plugin trust files of §4.13.

**Validation happens before the first port opens**, and every problem is reported at once: a
missing variable does not cost one restart each. A process that would otherwise discover its
misconfiguration on the first use of a rare feature fails as a deployment error instead,
immediately and in front of the right person.

A value that does not parse (a number, a duration, a boolean) is not reported: its default
applies. `PIPELINE_INSECURE_REGISTRY` accepts only `true`.

Each role validates only what it uses. In the tables below, **Required** means required
*for the roles listed*; elsewhere the variable is optional.

## 4.1 HTTP (role `api`)

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `HTTP_ADDR` | `:8080` | api | Listen address |
| `PUBLIC_URL` | — | **api** | Public base URL. Redirect URLs are built from it |
| `HTTP_READ_TIMEOUT` | `15s` | | Request read deadline |
| `HTTP_WRITE_TIMEOUT` | `60s` | | Response write deadline. Streaming routes are exempt |
| `HTTP_SHUTDOWN_TIMEOUT` | `20s` | | Grace period on SIGTERM |
| `MCP_ENABLED` | `true` | | Serves the MCP server at `/mcp` and the OAuth server that authorises AI agents on it (doc 09 §9.12). `false`: neither is served, nor the routes of the consent screen and of the *Agents IA* tab |

`PUBLIC_URL` is also read by the worker, unvalidated there: it is the base of the links a commit
status carries back to the dashboard.

## 4.2 Database (all roles but `check-tracker`)

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `DATABASE_DSN` | — | **all but check-tracker** | PostgreSQL DSN. Carries a password: it is redacted in logs and errors |
| `DATABASE_MAX_CONNS` | `10` | | Pool size **per process**, must be ≥ 1 |
| `DATABASE_CONN_MAX_LIFETIME` | `1h` | | Connection recycling |

Nothing shares `DATABASE_MAX_CONNS` between processes: each `api` replica and the worker open their
own pool. A plugin that keeps tables opens one more in each of these processes, outside this
variable; the deploy plugin bounds its own to 4. The reference deployment can therefore hold
`(2 api + 1 worker) × (10 + 4) = 42` connections, a pod more during a rolling update, plus what the
short-lived roles (`migrate`, `config`, `reprocess`) open. Keep that total under PostgreSQL's
`max_connections`, less its reserved connections. `10` is a starting value, not a measured one: the
pool only needs to grow if requests wait for a connection.

## 4.3 Object store (roles `api`, `worker`, `reprocess`, `config`)

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `MINIO_ENDPOINT` | — | **api, worker, reprocess, config** | `host:port`, no scheme |
| `MINIO_ACCESS_KEY` | — | **idem** | |
| `MINIO_SECRET_KEY` | — | **idem** | |
| `MINIO_BUCKET` | `arkhitekton` | | Bucket holding logs, reports, attachments and plugins |
| `MINIO_REGION` | `us-east-1` | | Must match the server. A wrong region invalidates the SigV4 signature, with a server message that does not say so |
| `MINIO_USE_SSL` | `true` | | |
| `MINIO_PRESIGN_TTL` | `6h` | | Lifetime of the upload URLs handed to pipelines. Must outlast the longest pipeline: below `90m`, start-up is refused |

## 4.4 Authentication (role `api`)

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `OIDC_ISSUER_URL` | — | **api** | Must match the `iss` claim exactly, trailing slash included |
| `OIDC_CLIENT_ID` | — | **api** | |
| `OIDC_CLIENT_SECRET` | — | **api** | |
| `OIDC_REDIRECT_URL` | `<PUBLIC_URL>/api/v1/auth/callback` | | To set only if the provider must call back another address. The provider's client declares this URL |
| `OIDC_SCOPES` | `openid,profile,email` | | Comma-separated. Add `groups` for the provider to say which groups a user belongs to (Gitea: organisations and `organisation:team`), so that a group can be given a role |
| `OIDC_GROUPS_CLAIM` | `groups` | | The ID token's claim carrying those groups: a list of names, or one name. Gitea keeps the scopes of an application already authorised: after adding `groups` to an existing instance, each user revokes the dashboard in their forge settings (Applications) and signs in again, or their token carries no groups. A sign-in logs `groups_claimed=false` when the claim is absent |
| `SESSION_TTL` | `12h` | | Session lifetime. Must be positive |
| `BOOTSTRAP_ADMINS` | — | **api** | Subjects or usernames promoted to administrator at first sign-in. Applied once, by `migrate`; the api refuses to start while it is empty |
| `BOOTSTRAP_USERS` | — | | `alice:releaser,bob:operator`; roles `viewer`, `operator`, `releaser`, `admin`, an entry with another role being ignored. Applied once, by `migrate`; the database is authoritative afterwards |

## 4.5 Tekton and the cluster (role `worker`; the catalogue's path for the api too)

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `TEKTON_NAMESPACE` | — | **worker** | Where PipelineRuns are created. The reference deployment sets `arkhitekton-pipelines` |
| `POD_NAMESPACE` | `arkhitekton` | **worker** | Namespace of the leader `Lease`. Deliberately not the Tekton namespace, where the service account may not write |
| `POD_NAME` | host name + `-local` | | Identity in the leader `Lease`. The base Deployment injects it from `metadata.name` |
| `PIPELINE_CATALOG_PATH` | `/etc/arkhitekton/catalog.yaml` | | Catalogue location |
| `KUBECONFIG` | — | | Outside a cluster only; in-cluster credentials are used when empty |
| `TEKTON_DASHBOARD_URL` | — | | Root of a Tekton Dashboard, if deployed. Empty hides the link |
| `PIPELINE_GIT_SECRET` | `git-credentials` | | Secret holding the deployment's git credentials, for the repositories that are not a project's own (the end-to-end campaign's). A project's repository is cloned with its own deploy key. Must exist in `TEKTON_NAMESPACE` |
| `PIPELINE_REGISTRY_SECRET` | `regcred` | | Secret holding the registry credentials the pipelines push with. Must exist in `TEKTON_NAMESPACE` |
| `PIPELINE_IMAGE_PULL_SECRET` | `regcred` | | Pull secret of the pipelines' tooling images. Must exist in `TEKTON_NAMESPACE` |
| `PIPELINE_MINIO_HOST` | — | | Object store as pipelines reach it (`minio.host`). A pipeline requiring it is refused while empty. Today, `deploy/integration-tests` and `deploy/security-audit`, from the deploy plugin; the other pipelines publish through the dashboard and need neither variable |
| `PIPELINE_MINIO_BUCKET` | — | | Bucket those pipelines write to (`minio.pipelineBucket`), likewise. It is not `MINIO_BUCKET`: the dashboard neither reads nor purges it, and its retention is the store's lifecycle rules |
| `FORGE_TEKTON_REPO_URL` | — | | Repository of the pipelines' definitions (`forge.tekton.repoUrl`). When set, a fresh instance seeds a project for it, with no image; no shipped pipeline reads it |
| `FORGE_SSH_PORT` | `2222` | | SSH port used to build a project's clone URL. It cannot be derived from an HTTPS forge URL |

## 4.6 Issue tracker (Redmine)

Read-only, enforced in the transport: any method other than GET or HEAD is refused
before the request leaves the process.

`TRACKER_URL` and `TRACKER_API_KEY` **seed** the tracker plugin's settings, and
`TRACKER_PROJECT` the first project's tracker, once, when the `migrate` Job first runs (§4.11).
The settings are then edited under Administration → Plugins. `TRACKER_TIMEOUT` stays a
deployment value, and so does `TRACKER_REVIEW_STATUSES`: the default for the projects that
declare no review statuses.

| Variable | Default | Meaning |
|---|---|---|
| `TRACKER_URL` | — | Base URL |
| `TRACKER_API_KEY` | — | Sent as `X-Redmine-API-Key`, never in a query string |
| `TRACKER_PROJECT` | — | Project identifier as it appears in the URL |
| `TRACKER_TIMEOUT` | `15s` | |
| `TRACKER_REVIEW_STATUSES` | `En revue` | Statuses that make an issue eligible for a release note, for a project that declares none. The vocabulary belongs to the deployment |

`check-tracker` requires `TRACKER_URL` and `TRACKER_API_KEY` and nothing else: it exists
to test a key without waiting for a screen to use it.

## 4.7 Forge (Gitea)

`FORGE_URL` and `FORGE_TOKEN` seed the forge plugin's settings, and `FORGE_REPO` the first
project's repository, once, in the `migrate` Job; the timeout stays a deployment value. A
project's repository is its own, declared with the project.

| Variable | Default | Meaning |
|---|---|---|
| `FORGE_URL` | — | Base URL |
| `FORGE_TOKEN` | — | The plugin writes with it: tags, commit statuses, comments, releases, committed version files and deploy keys. It needs `write:repository`, from an account that administers the projects' repositories |
| `FORGE_REPO` | — | `owner/name` |
| `FORGE_TIMEOUT` | `10s` | |

## 4.8 Registry

The dashboard's processes only read the registry: digests, published images, plugins. A read-only
account is enough for the variables below.

Images are pushed by the pipelines, `docker/build` and `docker/retag`, with the credentials bound to
their `registry-push` need: `PIPELINE_REGISTRY_SECRET` (§4.5) unless the project's `docker` setting
`registrySecret` or a binding names another Secret.

| Variable | Default | Meaning |
|---|---|---|
| `REGISTRY_URL` | — | Needed with `REGISTRY_USERNAME` and `REGISTRY_PASSWORD`: with one of the three empty, the registry is not read |
| `REGISTRY_USERNAME` | — | |
| `REGISTRY_PASSWORD` | — | |
| `REGISTRY_PROJECT` | — | Repository prefix, host included (`registry.example.com/team`): where images go when the `docker` plugin's `registry` setting names none, at the instance and at the project ([§6](06-plugins.md)) |
| `PIPELINE_INSECURE_REGISTRY` | `false` | `true` when `REGISTRY_PROJECT` is served over plain HTTP, a local registry: the build and re-tag pipelines then push without TLS (`registry.insecure`) |

## 4.9 Probes and webhooks

| Variable | Default | Meaning |
|---|---|---|
| `PROBE_INTERVAL` | `5m` | Polling period of the tracker and forge probes. A floor is applied when building the loop: declaring ten seconds does not obtain them |
| `WEBHOOK_ALLOW_PRIVATE` | `false` | Allows private and loopback targets, below. Leave it off unless an HTTP action must reach an internal service |
| `WEBHOOK_ALLOW_INSECURE` | `false` | Allows `http://`. The payload carries a release tag and the token that authenticates it |
| `WEBHOOK_TIMEOUT` | `15s` | |

The `WEBHOOK_*` variables bound the URLs typed into an HTTP action, issued from inside the cluster.
A plugin's steps (Gitea, Rocket.Chat) are not concerned: their target is the plugin's configuration.

`WEBHOOK_ALLOW_PRIVATE` reopens the loopback, the private ranges (`10.0.0.0/8`, `172.16.0.0/12`,
`192.168.0.0/16`, `fc00::/7`) and `100.64.0.0/10`, hence the cluster's services and its API. It never
reopens link-local (`169.254.0.0/16`, `fe80::/10`), where `169.254.169.254` serves cloud instance
metadata, nor multicast.

## 4.10 Secrets at rest

| Variable | Default | Meaning |
|---|---|---|
| `SECRETS_KEY` | — | 32 bytes, base64: `openssl rand -base64 32`. Seals the secret fields of plugin settings |
| `SECRETS_RETIRED_KEYS` | — | Previous keys, which still open what they sealed. A rotation runs in two stages, because values are re-sealed only as they are rewritten |

Its absence does not prevent the api and the worker from starting; it makes saving a secret
field impossible, and the interface says so where the field is typed. It is required, however,
as soon as `FORGE_TOKEN` or `TRACKER_API_KEY` is set: the `migrate` Job seals them, and fails
without it. A key that does not decode to 32 bytes refuses start-up.

**The key lives in the environment, not in the database**: that is the point. A database
backup says nothing without it, and everything with it, so it must be backed up
elsewhere.

## 4.11 Plugin seeds

Plugin settings live in the database and are edited from the interface. The tracker and forge
variables seed them once, in the `migrate` Job, and **only what is missing**: a value changed
from the administration screen is not overwritten by the next deployment.

An installed plugin's settings are set from the administration screen, and the plugin documents
them.

A whole configuration (projects, plugins, automations) can also be applied from versioned files,
which supersedes these seeds for a new instance: see [§10.9](10-operations.md#109-the-configuration-as-files).

`PROJECT_DOCKERFILE` (default `Dockerfile`) and `PROJECT_BUILD_CONTEXT` (default `.`) seed
the first project's `docker` plugin settings, which the project then owns. Its repository is derived
from `FORGE_URL` and `FORGE_REPO`, or given whole by `FORGE_REPO_URL`; with neither, no
project is seeded. `FORGE_TEKTON_REPO_URL`, when set, also seeds a project for the pipelines'
repository, with no image.

## 4.12 Renamed and removed variables

Start-up fails, naming the replacement, if a retired name is set. Silence would leave a
deployment running with a setting it believes it applied.

| Retired | Replacement |
|---|---|
| `GITEA_URL`, `GITEA_TOKEN`, `GITEA_REPO`, `GITEA_TIMEOUT` | `FORGE_*` |
| `REDMINE_URL`, `REDMINE_API_KEY`, `REDMINE_PROJECT`, `REDMINE_TIMEOUT`, `REDMINE_REVIEW_STATUSES` | `TRACKER_*` |
| `DATABASE_URL` | `DATABASE_DSN`. Plugins still read `DATABASE_URL`, which the dashboard sets for them |
| `PIPELINE_GIT_PUBLIC_KEY` | Removed: the shared deploy key no longer exists, each project has its own |

## 4.13 Installed plugins (role `worker`)

The worker pulls the plugins an administrator installs (§6.12). What it trusts, and how it
reaches private registries, are files the operator mounts: the administration screen can change
neither.

| Variable | Default | Meaning |
|---|---|---|
| `PLUGIN_TRUSTED_KEYS` | none | PEM file of the public keys, ECDSA or Ed25519, a plugin must be signed with. Unset or absent, no signature is recognised: a plugin then installs only through `PLUGIN_ALLOW_UNSIGNED` or an administrator accepting its source (`PLUGIN_OFFER_UNRECOGNISED`). Read at every installation: a key added or withdrawn applies without a restart |
| `PLUGIN_REGISTRY_AUTH` | none | Docker `config.json` holding the credentials of private registries. Its `auths` are read; credential helpers are not run. Absent, registries are reached anonymously |
| `PLUGIN_PLAIN_HTTP_REGISTRIES` | none | Comma-separated registries reached without TLS: a registry inside a development cluster |
| `PLUGIN_ALLOW_UNSIGNED` | none | Comma-separated plugin ids installed without a trusted signature, still pinned by digest. The installation records them as `unsigned`, and the screen says so. With this list set, `PLUGIN_TRUSTED_KEYS` may be absent: then only these install |
| `PLUGIN_UPDATE_INTERVAL` | `6h` | How often the worker reads the installed plugins' repositories for a newer version. *Vérifier maintenant* (check now) on the Plugins screen checks one within seconds |
| `PLUGIN_OFFER_UNRECOGNISED` | `true` | A plugin from a source the instance does not recognise waits for an administrator rather than being refused: the screen warns, and accepting records the plugin's id so its next version installs without asking. Installing a plugin is the administrator's act, and they already pin the digest. Set to `false` for an instance that would rather a compromised administrator account could install nothing: the refusal is then final, and only the trusted keys and `PLUGIN_ALLOW_UNSIGNED` let a plugin in |

The base deployment sets the first two to optional mounts:

| Mount | Source | Content |
|---|---|---|
| `/etc/arkhitekton-plugins/keys/trusted-keys.pem` | ConfigMap `arkhitekton-plugin-keys` | the trusted public keys |
| `/etc/arkhitekton-plugins/registry/config.json` | Secret `arkhitekton-plugin-registry`, key `.dockerconfigjson` | registry credentials |

```sh
kubectl -n arkhitekton create configmap arkhitekton-plugin-keys --from-file=trusted-keys.pem
kubectl -n arkhitekton create secret docker-registry arkhitekton-plugin-registry \
  --docker-server=registry.example --docker-username=… --docker-password=…
```

An installed plugin's program (doc 06 §6.4) runs in the api and worker pods: it is written to
their `/tmp` (an `emptyDir` of 1 GiB in the base deployment), which must allow execution, and it
shares the pod's memory limit. Raise the limits for heavy plugins.

