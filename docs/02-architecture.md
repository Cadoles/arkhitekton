# 2. Architecture

## 2.1 One binary, five roles

Arkhitekton ships as a single binary. The first argument selects what the process does:

| Role | Long-running | What it does |
|---|---|---|
| `api` | yes, replicated | Serves the HTTP API and the web interface |
| `worker` | yes, single active instance | Runs the background loops |
| `migrate` | no | Applies schema migrations, then exits |
| `reprocess` | no | Re-reads raw pipeline artifacts and rebuilds results |
| `check-tracker` | no | Verifies tracker credentials read-only, then exits |

One binary means one image and one version: the API and the worker can never disagree
about the schema they expect, because they are the same build.

Each role validates only the configuration it uses — an `api` without `TEKTON_NAMESPACE`
starts, a `worker` without it does not. See [04 — Configuration](04-configuration.md).

## 2.2 Processes and state

    ┌────────────┐      ┌──────────────┐
    │  browser   │──────│  api (n×)    │────────┐
    └────────────┘      └──────────────┘        │
                                                │   ┌──────────────┐
    ┌────────────┐      ┌──────────────┐        ├───│  PostgreSQL  │
    │   Tekton   │──────│ worker (1×)  │────────┤   └──────────────┘
    │  Pipelines │      └──────────────┘        │   ┌──────────────┐
    └────────────┘             │                └───│  object store│
                               │                    └──────────────┘
                     tracker, forge, registry

**PostgreSQL holds all state.** Queue, releases, results, sessions, audit log, plugin
settings. Nothing important lives in a process's memory: an API pod can be replaced at
any moment.

**The object store holds what is large.** Pipeline logs, raw reports, exported release
notes. The database keeps the object key, never the payload.

**The API is stateless and replicated.** Sessions are database-backed; readiness is per
dependency (see [10 §10.3](10-operations.md#103-health-and-readiness)).

**The worker is single-active.** Several replicas may run, but only the holder of a
Kubernetes `Lease` executes the loops. Losing the lease stops the loops without killing
the process; regaining it restarts them.

## 2.3 The worker's loops

Under the lease, the worker runs these concurrently; the first one to fail ends the
leadership term, and the loops restart on the next one.

| Loop | Period | Responsibility |
|---|---|---|
| **scheduler** | on tick and on demand | Dequeues jobs, composes their settings, creates Tekton PipelineRuns |
| **observer** | on tick | Reconciles PipelineRuns with the queue: concludes jobs, collects logs and results |
| **purger** | on tick | Deletes expired sessions, auth flows and stale runs |
| **key reconciler** | on tick | Keeps each project's deploy-key secret in sync with what the project declares |
| **capability reconciler** | at election, then on tick | Installs the bound capabilities' pipeline definitions, leaving foreign ones alone, then checks what the cluster can run; a pipeline it cannot run is refused with the reason |
| **probes** | `PROBE_INTERVAL` | Polls the tracker and the forge for facts (merged pull requests, reviewed versions) and dispatches reactions |
| **received facts** | 2 s | Routes the facts a webhook brought to the `api` to the project's reactions |
| **schedules** | 20 s | Opens the plans of the schedules that are due; one run per tick, none while the previous one goes, none for a tick more than 15 minutes late |
| **modules** | 30 s | Runs the modules installed plugins bring, while their plugin is switched on ([06 §6.9](06-plugins.md#69-modules)) |

## 2.4 What talks to what

**Tekton.** The worker creates `PipelineRun` objects in `TEKTON_NAMESPACE` and labels
them with the job id, the kind, the release and the instance identity. Nothing else in
the cluster is written. Runs created outside Arkhitekton — by a webhook or a CronJob —
carry no such label and are observed, not driven.

**The object store.** Pipelines never receive store credentials: the dashboard mints
presigned URLs for the exact objects a run may write, and the run uploads to them.

**The tracker (Redmine).** Read-only, enforced in the transport: any method other than
GET or HEAD is refused before the request leaves the process.

**The forge (Gitea).** Read-only for browsing refs, with one exception that is explicit
and separate: deploy keys, which Arkhitekton creates and rotates per project.

**The registry.** Read-only: digests, tags, manifests. Arkhitekton never pushes an image;
promotion re-tags through a pipeline that carries its own credentials.

## 2.5 The plugin boundary

The core never imports a plugin. Plugins register a `Descriptor` — settings, events,
actions, routes — and the core knows only descriptors. A Go test enforces the direction
of the dependency, and the same rule holds in the front-end, where a single module file
is allowed to import feature code.

The consequence is that an instance with every plugin disabled is still coherent: screens
that belong to a plugin disappear, nothing else changes. See [06 — Plugins](06-plugins.md).

What a plugin's pipelines cannot bring, routes, tables, loops and screens, comes from its
**program**: a separate process the dashboard launches and talks to over gRPC, and an interface
script that takes React from the page, both installed from a registry without recompiling. See
[06, §6.4](06-plugins.md).

## 2.6 The web interface

React, built with Vite, embedded into the binary and served by the `api` role at `/`.
There is no separate front-end deployment, and no CORS configuration to get wrong: the
interface and the API share an origin. API routes live under `/api/v1`.
