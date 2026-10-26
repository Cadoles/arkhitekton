# 1. Overview

Arkhitekton is a release dashboard. It sits between an issue tracker, a forge, a CI/CD
engine and a container registry, and answers one question that none of them answers on
its own: **what is the state of this version, and what remains to be done before it
ships?**

It does not replace any of those systems. It reads them, correlates what they say around
a release, and lets a human take the next step — run the missing pipeline, read a failed
test, write the release note, promote an image — without leaving one screen.

## 1.1 What it manages

**Projects.** A project is a repository that produces images, with its own tracker
project, forge repository, registry namespace, deploy key and pipeline settings. One
Arkhitekton instance serves several projects; nothing is global except the instance
itself.

**Versions.** A version is what the tracker calls a milestone: a set of issues meant to
ship together. Arkhitekton mirrors versions and their issues, so that a release can be
built from them, and works without a tracker at all — versions can be created locally.

**Releases.** A release is a tag of a project's repository, built and verified. It carries
the images it produced, the results of every pipeline that ran against it, its release
notes, and the artifacts it exported.

**Jobs and runs.** A job is a request to run a pipeline: queued, dispatched to Tekton,
observed until it concludes. A run is what Tekton did with it — its tasks, its logs, its
results.

**Plans.** A plan is what a release still owes: an ordered set of stages, each a set of
pipeline runs, derived from what the project declares it needs. The plan advances on its
own as jobs conclude.

**Events and reactions.** Anything worth reacting to is recorded as an event — a build
succeeded, a version was reviewed, a plugin observed a promotion. A reaction is a
declarative rule that turns an event into an action: run a pipeline, call a webhook,
start a plan.

**Plugins.** Everything domain-specific lives in a plugin. The core knows nothing about
GitOps, deployment observation or sign-offs; a plugin installed on the instance
adds them for one way of deploying. See [06 — Plugins](06-plugins.md).

## 1.2 What it needs

At minimum: a PostgreSQL database, an S3-compatible object store, a Kubernetes cluster
running Tekton Pipelines, and an OIDC provider to authenticate users.

Optional, each degrading gracefully when absent: an issue tracker (Redmine), a forge API
(Gitea), a container registry.

[03 — Installation](03-installation.md) states versions and required access.

## 1.3 How a release moves

1. A version is created, or mirrored from the tracker, and gathers issues.
2. A release is created from a git ref. Arkhitekton records the tag and the commit.
3. The project's declared plan starts: build, then the tests and audits it requires.
4. Each job is queued, dispatched to Tekton, and observed. Its results — test cases,
   security findings, benchmark measures, artifacts — are read back from the object
   store and the pipeline's own output.
5. The release note is assembled from the issues of the version, edited if needed, and
   exported.
6. A release that satisfies its plan becomes final; what happens next — promotion to an
   environment, sign-off, GitOps deployment — is a plugin's business.

## 1.4 Vocabulary

| Term | Meaning |
|---|---|
| **kind** | A pipeline the catalogue can launch, named by the capability it binds (`core/build`, `php/unit-tests`, `security/security-audit`, …). Plugins bring capabilities; the core names only `core/build` and `core/retag`. |
| **catalogue** | The YAML file that declares which pipelines exist, what they need and what they produce. See [05](05-pipeline-catalogue.md). |
| **geste** | One pipeline run inside a plan stage. |
| **fact** | An event normalised to a type a reaction can match. |
| **module** | A plugin of kind `module`: it adds screens and routes, and can be switched off per project. |

## 1.5 Documentation map

| Document | Contents |
|---|---|
| [01 — Overview](01-overview.md) | This document |
| [02 — Architecture](02-architecture.md) | Processes, components, data flow |
| [03 — Installation](03-installation.md) | Prerequisites, deployment, bootstrap |
| [04 — Configuration](04-configuration.md) | Every environment variable |
| [05 — Pipeline catalogue](05-pipeline-catalogue.md) | Catalogue format and the contract a pipeline honours |
| [06 — Plugins](06-plugins.md) | Plugin model, settings, per-project activation |
| [07 — HTTP API](07-http-api.md) | Every route, its permission and its shape |
| [08 — Data model](08-data-model.md) | PostgreSQL schema and object layout |
| [09 — Security](09-security.md) | Authentication, roles, RBAC, secrets |
| [10 — Operations](10-operations.md) | Migrations, readiness, leader election, troubleshooting |
| [11 — Development](11-development.md) | Building, testing, local cluster |
