# 4. Configuration

Everything is read from the environment, once, at start-up. There is no configuration
file except the pipeline catalogue.

**Validation happens before the first port opens**, and every problem is reported at
once — a missing variable does not cost one restart each. A process that would otherwise
discover its misconfiguration at a user's rarest click fails as a deployment error
instead, immediately and in front of the right person.

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

## 4.2 Database (all roles but `check-tracker`)

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `DATABASE_URL` | — | **all but check-tracker** | PostgreSQL DSN. Carries a password: it is redacted in logs and errors |
| `DATABASE_MAX_CONNS` | `10` | | Pool size, must be ≥ 1 |
| `DATABASE_CONN_MAX_LIFETIME` | `1h` | | Connection recycling |

## 4.3 Object store (roles `api`, `worker`, `reprocess`)

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `MINIO_ENDPOINT` | — | **api, worker, reprocess** | `host:port`, no scheme |
| `MINIO_ACCESS_KEY` | — | **idem** | |
| `MINIO_SECRET_KEY` | — | **idem** | |
| `MINIO_BUCKET` | `arkhitekton` | | Bucket holding logs, reports and exports |
| `MINIO_REGION` | `NUO-Region` | | Must match the server. A wrong region invalidates the SigV4 signature, with a server message that does not say so |
| `MINIO_USE_SSL` | `true` | | |
| `MINIO_PRESIGN_TTL` | `2h` | | Lifetime of the upload URLs handed to pipelines. Must outlast the longest pipeline |

## 4.4 Authentication (role `api`)

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `OIDC_ISSUER_URL` | — | **api** | Must match the `iss` claim exactly, trailing slash included |
| `OIDC_CLIENT_ID` | — | **api** | |
| `OIDC_CLIENT_SECRET` | — | **api** | |
| `OIDC_REDIRECT_URL` | — | **api** | `<PUBLIC_URL>/api/v1/auth/callback` |
| `OIDC_SCOPES` | `openid,profile,email` | | Comma-separated |
| `SESSION_TTL` | `12h` | | Session lifetime |
| `BOOTSTRAP_ADMINS` | — | | Subjects or usernames promoted to administrator at first sign-in |
| `BOOTSTRAP_USERS` | — | | `alice:releaser,bob:operator`. Read once; the database is authoritative afterwards |

## 4.5 Tekton and the cluster (role `worker`)

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `TEKTON_NAMESPACE` | — | **worker** | Where PipelineRuns are created. The reference deployment sets `arkhitekton-pipelines` |
| `POD_NAMESPACE` | `arkhitekton` | **worker** | Namespace of the leader `Lease`. Deliberately not the Tekton namespace, where the service account may not write |
| `PIPELINE_CATALOG_PATH` | `/etc/arkhitekton/catalog.yaml` | | Catalogue location |
| `KUBECONFIG` | — | | Outside a cluster only; in-cluster credentials are used when empty |
| `TEKTON_DASHBOARD_URL` | — | | Root of a Tekton Dashboard, if deployed. Empty hides the link |
| `PIPELINE_GIT_SECRET` | — | | Secret holding the deployment's git credentials, for the repositories that are not a project's own (the end-to-end campaign's). A project's repository is cloned with its own deploy key |
| `PIPELINE_REGISTRY_SECRET` | — | | Secret holding registry credentials for pipelines |
| `PIPELINE_IMAGE_PULL_SECRET` | — | | Pull secret added to pipeline pods |
| `PIPELINE_MINIO_HOST` | — | | Object store as pipelines reach it (`minio.host`). A pipeline requiring it is refused while empty |
| `PIPELINE_MINIO_BUCKET` | — | | Bucket pipelines write to (`minio.pipelineBucket`), likewise |
| `FORGE_TEKTON_REPO_URL` | — | | Repository the security audit clones its rules from (`forge.tekton.repoUrl`), likewise |
| `FORGE_SSH_PORT` | forge default | | SSH port used to build a project's clone URL. It cannot be derived from an HTTPS forge URL |

## 4.6 Results

| Variable | Default | Meaning |
|---|---|---|

## 4.7 Issue tracker (Redmine)

Read-only, enforced in the transport: any method other than GET or HEAD is refused
before the request leaves the process.

These variables **seed** the tracker plugin's settings at first start-up (§4.12); the
dashboard then reads the plugin's settings, edited under Administration → Plugins. Only the
timeout stays a deployment value.

| Variable | Default | Meaning |
|---|---|---|
| `TRACKER_URL` | — | Base URL |
| `TRACKER_API_KEY` | — | Sent as `X-Redmine-API-Key`, never in a query string |
| `TRACKER_PROJECT` | — | Project identifier as it appears in the URL |
| `TRACKER_TIMEOUT` | `15s` | |
| `TRACKER_REVIEW_STATUSES` | `En revue` | Statuses that make an issue eligible for a release note. The vocabulary belongs to the deployment |

`check-tracker` requires `TRACKER_URL` and `TRACKER_API_KEY` and nothing else: it exists
to test a key without waiting for a screen to use it.

## 4.8 Forge (Gitea)

Like the tracker's, these variables seed the forge plugin's settings; the timeout stays a
deployment value. A project's repository is its own, declared with the project.

| Variable | Default | Meaning |
|---|---|---|
| `FORGE_URL` | — | Base URL |
| `FORGE_TOKEN` | — | A `read:repository` scope is enough, and is what should be issued |
| `FORGE_REPO` | — | `owner/name` |
| `FORGE_TIMEOUT` | `10s` | |

## 4.9 Registry

Read-only. Arkhitekton never pushes an image.

| Variable | Default | Meaning |
|---|---|---|
| `REGISTRY_URL` | — | Empty, the registry is not read |
| `REGISTRY_USERNAME` | — | |
| `REGISTRY_PASSWORD` | — | |
| `REGISTRY_PROJECT` | — | Repository prefix, host included (`registry.example.com/team`) |
| `REGISTRY_IMAGE` | — | The repository that promotion re-tags; empty, the delivery note names no image |

## 4.10 Probes and webhooks

| Variable | Default | Meaning |
|---|---|---|
| `PROBE_INTERVAL` | `5m` | Polling period of the tracker and forge probes. A floor is applied when building the loop: declaring ten seconds does not obtain them |
| `WEBHOOK_ALLOW_PRIVATE` | `false` | Allows private and loopback targets. A local deployment needs it; a production one does not. Link-local is never reopened |
| `WEBHOOK_ALLOW_INSECURE` | `false` | Allows `http://`. The payload carries a release tag and the token that authenticates it |
| `WEBHOOK_TIMEOUT` | `15s` | |

## 4.11 Secrets at rest

| Variable | Default | Meaning |
|---|---|---|
| `SECRETS_KEY` | — | 32 bytes, base64: `openssl rand -base64 32`. Seals the secret fields of plugin settings |
| `SECRETS_RETIRED_KEYS` | — | Previous keys, which still open what they sealed. A rotation runs in two stages, because values are re-sealed only as they are rewritten |

Its absence does not prevent start-up; it makes saving a secret field impossible, and the
interface says so where the field is typed. Refusing to start would punish a deployment
that has nothing to protect yet.

**The key lives in the environment, not in the database** — that is the point. A database
backup says nothing without it, and everything with it, so it must be backed up
elsewhere.

## 4.12 Plugin seeds

Plugin settings live in the database and are edited from the interface. These variables
seed them at first start-up, and **only what is missing**: a value changed from the
administration screen is not overwritten by the next restart.

An installed plugin's settings are set from the administration screen, and the plugin documents
them.

`PROJECT_DOCKERFILE` (default `Dockerfile`) and `PROJECT_BUILD_CONTEXT` (default `.`) seed
the first project's build settings, which the project then owns. Its repository is derived
from `FORGE_URL` and `FORGE_REPO`, or given whole by `FORGE_REPO_URL`; with neither, no
project is seeded.

## 4.13 Renamed and removed variables

Start-up fails, naming the replacement, if a retired name is set. Silence would leave a
deployment running with a setting it believes it applied.

| Retired | Replacement |
|---|---|
| `GITEA_URL`, `GITEA_TOKEN`, `GITEA_REPO`, `GITEA_TIMEOUT` | `FORGE_*` |
| `REDMINE_URL`, `REDMINE_API_KEY`, `REDMINE_PROJECT`, `REDMINE_TIMEOUT`, `REDMINE_REVIEW_STATUSES` | `TRACKER_*` |
| `FORGE_MSE_REPO_URL`, `MSE_DOCKERFILE`, `MSE_BUILD_CONTEXT` | `FORGE_REPO_URL`, `PROJECT_DOCKERFILE`, `PROJECT_BUILD_CONTEXT` |
| `PIPELINE_GIT_PUBLIC_KEY` | Removed: the shared deploy key no longer exists, each project has its own |

## 4.14 Installed plugins (role `worker`)

The worker pulls the plugins an administrator installs (§6.12). What it trusts, and how it
reaches private registries, are files the operator mounts: the administration screen can change
neither.

| Variable | Default | Meaning |
|---|---|---|
| `PLUGIN_TRUSTED_KEYS` | none | PEM file of the public keys, ECDSA or Ed25519, a plugin must be signed with. Unset or absent, no plugin installs, and the installation says why. Read at every installation: a key added or withdrawn applies without a restart |
| `PLUGIN_REGISTRY_AUTH` | none | Docker `config.json` holding the credentials of private registries. Its `auths` are read; credential helpers are not run. Absent, registries are reached anonymously |
| `PLUGIN_PLAIN_HTTP_REGISTRIES` | none | Comma-separated registries reached without TLS: a registry inside a development cluster |
| `PLUGIN_ALLOW_UNSIGNED` | none | Comma-separated plugin ids installed without a trusted signature, still pinned by digest. The installation records them as `unsigned`, and the screen says so. With this list set, `PLUGIN_TRUSTED_KEYS` may be absent: then only these install |

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

