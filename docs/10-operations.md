# 10. Operations

## 10.1 Schema migrations

Migrations are embedded in the binary; the database can never be migrated by a version
other than the one that ships them.

    arkhitekton migrate

The reference deployment runs this as a `Job` before the Deployments roll. `api` and
`worker` do not assume it succeeded: at start-up each waits up to **45 seconds** for the
database to carry the migrations its own build expects, and exits if it does not. A pod
that started against an older schema would fail later, in a request, instead of at
deployment time.

**A plugin owns its migrations.** The MSE plugin keeps its own history in
`goose_mse_db_version`, applied by the same command and waited on the same way. The
core's migration history never mentions plugin tables.

Rolling back is a plugin-level or core-level operation on its own history; there is no
combined rollback, by design.

## 10.2 Leader election

Several `worker` replicas may run; only one executes the loops. Arbitration is a
Kubernetes `Lease` named `arkhitekton-worker`, in `POD_NAMESPACE`:

| Setting | Value |
|---|---|
| Lease duration | 30 s |
| Renew deadline | 20 s |
| Retry period | 5 s |

Losing the lease stops the loops without killing the process; regaining it restarts them.
A worker that cannot reach the API server therefore stops working rather than working in
parallel with another one.

## 10.3 Health and readiness

| Endpoint | Meaning |
|---|---|
| `GET /healthz` | The process is alive. Suitable as a liveness probe |
| `GET /readyz` | Every dependency answers. Suitable as a readiness probe |

`/readyz` reports one entry per check: `postgres`, `schema`, `minio`, and one per plugin
with a schema of its own (`schema-mse`). A single failing check makes the pod
unready — it stops receiving traffic without being restarted, which is the right response
to a dependency that is briefly unavailable.

## 10.4 Loop cadence

| Loop | Cadence |
|---|---|
| Scheduler | 2 s, or immediately when a job is enqueued |
| Observer | 2 s |
| Purge (sessions, auth flows) | 15 min |
| Environments (MSE plugin) | 30 s |
| Probes (tracker, forge) | `PROBE_INTERVAL`, default 5 min, with an enforced floor |

The environment cadence is not tuneable and does not need to be: a GitOps deployment
takes minutes, so polling faster would learn nothing and multiply API calls.

## 10.5 Logs

Structured JSON on stdout (`slog`), with `version` and `revision` on every line. Messages
are currently French; keys are stable and are what monitoring should match on.

Four lines at worker start-up are worth watching, in order: the catalogue that was
loaded, the pipeline target, the modules enabled per project, and the candidacy for the
lease. Together they separate a misconfiguration from a failure faster than anything else.

## 10.6 Occasional roles

**Replaying results.** When a parser is fixed, the raw artifacts are still in the object
store:

    arkhitekton reprocess

It rebuilds results from what pipelines uploaded, without re-running anything.

**Checking tracker access.**

    arkhitekton check-tracker

Reads the tracker with the configured key and exits. It exists because the key targets a
production instance: the alternative is learning that a key was revoked from a user's
failed screen.

## 10.7 Backups

| What | Where | Note |
|---|---|---|
| Database | PostgreSQL | Everything except large payloads |
| Objects | Bucket | Logs, raw reports, exports |
| `SECRETS_KEY` | **Outside both** | A database backup is unreadable without it, and restoring one without the key leaves settings nothing can open |

## 10.8 Troubleshooting

**The worker crashes at start-up with an unreadable catalogue.** The catalogue ConfigMap
and the image roll separately: a new binary can start against an old ConfigMap and reject
a field it does not know. It resolves at the next rollout. Deploy the ConfigMap first when
changing the catalogue's format.

**The worker restarts with an `ON CONFLICT` error after a schema change.** Same race, the
other way round: the migration ran before the code that expects it. It resolves once the
new image is rolled.

**A pipeline fails on a missing parameter.** A `source: config` key that nothing resolves
makes the enqueue fail, naming the key. Either the project has not configured the plugin
that provides it, or the plugin is disabled for that project.

**Uploads fail near the end of long pipelines.** `MINIO_PRESIGN_TTL` must outlast the
longest pipeline; the URL is minted when the run starts.

**`iss` mismatch at login.** `OIDC_ISSUER_URL` must match the provider's claim exactly,
trailing slash included.

**Promotion is refused for an environment that exists.** The catalogue's `projects:`
section is keyed by project slug: an environment declared under the wrong slug is unknown
for that project, and unknown environments fail closed to the higher role.
