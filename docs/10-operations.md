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

**A plugin owns its migrations.** It keeps its own history in its own version table,
applied by the same command and waited on the same way. The
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

`/readyz` reports one entry per check: `postgres`, `schema`, `minio`, and one per installed plugin
with a schema of its own (`schema-<id>`). A single failing check makes the pod
unready — it stops receiving traffic without being restarted, which is the right response
to a dependency that is briefly unavailable.

## 10.4 Loop cadence

| Loop | Cadence |
|---|---|
| Scheduler | 2 s, or immediately when a job is enqueued |
| Observer | 2 s |
| Purge (sessions, auth flows) | 15 min |
| Module supervisor | 30 s |
| Probes (tracker, forge) | `PROBE_INTERVAL`, default 5 min, with an enforced floor |

The supervisor's cadence bounds how long switching a module on or off takes; what a module
does at its own pace is its own.

## 10.5 Logs

Structured JSON on stdout (`slog`), with `version` and `revision` on every line. Messages
and keys are English; the keys are stable and are what monitoring should match on.

Four lines at worker start-up are worth watching, in order: the catalogue that was
loaded, the pipeline target, the modules enabled per project, and the candidacy for the
lease. Together they separate a misconfiguration from a failure faster than anything else.

## 10.6 Occasional roles

**Replaying results.** When a parser or an interpreter is fixed, the raw artifacts are still
in the object store:

    arkhitekton reprocess --dry-run
    arkhitekton reprocess [--release <tag>]

It reads each run again through the interpreter of the capability that launched it, without
re-running anything, and pins load baselines again on the measures it rebuilds. A run another
plugin version read is refused, since reading it with the installed version changes its history;
`--accept-drift` replays it anyway. Results from before the plugins read their runs carry a
`-legacy` version and are always replayed.

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

## 10.9 The configuration as files

What an administrator sets in the interface (projects, plugin settings, a project's pipelines,
its release automation, reactions and schedules, queue limits, recorded bindings, installed
plugins) can be kept in versioned YAML files, tried on a local cluster, then applied to another
instance. The database stays authoritative: the interface remains editable, and an export reads
what it holds back into files.

    make config-export DIR=config/local          # one file per project, plus instance.yaml
    make config-diff   DIR=config/local          # what applying would write, nothing written
    make config-apply  DIR=config/local          # writes it
    make config-apply  DIR=config/local ARGS=--prune

The targets run `arkhitekton config export | diff | apply` in the worker's pod through
`kubectl exec` (set `KUBECONFIG` to choose the cluster): it already has the database, the
installed plugins, the catalogue and `SECRETS_KEY`. The image has no shell, so the files travel on
the standard streams; `arkhitekton config split -o <dir>` turns an exported stream into files on
the operator's machine.

```yaml
apiVersion: arkhitekton.config/v1
kind: Instance
spec:
  plugins:
    gitea:
      config: {url: https://forge.example, token: {env: ARK_GITEA_TOKEN}}
    mse:
      install: {reference: "forge.example/mse-plugin@sha256:…"}
  queueLimits: {mse/integration-tests: 1}
---
apiVersion: arkhitekton.config/v1
kind: Project
metadata: {name: portal}
spec:
  repository: team/portal
  stack: php
  plugins:
    mse: {config: {deployer: flux}}
  pipelines: {mse/e2e-tests: false}
  releaseAutomation:
    stages: [{role: build, gestes: [{kind: pipeline, ref: core/build}]}]
  schedules:
    - name: Integration
      cron: "0 8,12,16 * * 1-5"
      timezone: Europe/Paris
      ref: main
      plan: {stages: [{gestes: [{kind: pipeline, ref: mse/integration-tests}]}]}
```

A reaction on a pull request's fact builds and verifies its head the way a release's plan does,
its results showing on the pull request's page:

```yaml
  reactions:
    - name: Verify each push
      event: pull_request.synchronized
      match: {target_branch: main}
      plan:
        stages:
          - gestes: [{kind: pipeline, ref: core/build, params: {image-name: portal, target: web}}]
          - gestes: [{kind: pipeline, ref: php/static-tests}, {kind: pipeline, ref: php/unit-tests}]
```

Every image built that way stays in the registry under its `pr-<number>-<commit>` tag: the
registry's retention is the operator's.

Plans keep the shape the automation editor writes. Switches (`enabled`, a target's `promote`)
default to on. Unknown keys are refused, with the path of the mistake.

**What is declared is imposed in full; what is not declared is left alone.** A field omitted takes
its default, as at creation from the interface; a setting omitted from a plugin's `config` is
removed. `--prune` also removes, for the declared projects and the instance, the reactions,
schedules, project plugin settings, pipeline switches, status mappings, queue limits and bindings
the files no longer declare. It never removes a project nor uninstalls a plugin. Reactions and
schedules are matched by name: a rename is a removal and a creation, and the run history of the
old one keeps naming it by its id.

**Every write goes through the rules the interface applies**, and everything that can be checked
without writing (an unknown plugin, a kind missing from the catalogue, an unknown action or fact)
is checked before the first write. A failure in the middle names its operation; applying again
resumes from there. The deployer's version-scheme check is a hook of a running plugin and is only
applied from the interface.

**Sensitive settings are references, never values.** `{env: NAME}` reads the worker's
environment, `{file: PATH}` a file mounted in its pod. A resolved reference is sealed like a value
typed into the interface. An unresolved one keeps the value already recorded, so that applying an
export changes nothing; with no value recorded either, it is refused. A sensitive value can
therefore also be typed once in the interface, then kept by every apply. The export names each
sensitive setting `ARK_<PLUGIN>_<FIELD>`, or `ARK_<PROJECT>_<PLUGIN>_<FIELD>` for a project, and
lists the names on its error output.

**Installed plugins are installed first**, then the rest is planned against the registry they
complete: an installation is requested, pulled by the worker (`--wait-timeout`, 10 minutes by
default), then activated. A source the instance does not recognise is accepted only when the file
sets `acceptSource: true`.

The export writes what was decided, never what was observed (access verdicts, deploy keys, next
firings), and leaves out a plugin left as the dashboard seeds it. Roles, users, the catalogue's
environments and plugins' own tables are not part of it.
