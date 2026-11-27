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
notes, and the artifacts it exported. It starts as a **draft**: nothing is built, tagged or
automated until someone launches it, which leaves time to attach issues to its note.

**Pull requests.** The pull requests of a project's repository, followed from its forge: open,
closed and merged, each with the commit at its head. Pipelines are launched on a pull request as
on a release, its build included (images tagged `pr-<number>-<commit>`), and its page shows their
results, compared push with push. Its transitions are facts reactions listen for.

**Jobs and runs.** A job is a request to run a pipeline: queued, dispatched to Tekton,
observed until it concludes. A run is what Tekton did with it — its tasks, its logs, its
results.

**Plans.** A plan is what a release still owes: an ordered set of stages, each a set of
steps (a pipeline to run or an action to take), derived from what the project declares it needs. The plan advances on its
own as jobs conclude.

**Events and reactions.** Anything worth reacting to is recorded as an event — a build
succeeded, a version was reviewed, a plugin observed a promotion. A reaction is a
declarative rule that turns an event into an action: run a pipeline, call a webhook,
start a plan. A forge can push its events to the dashboard instead of being polled for them
([06 §6.5](06-plugins.md#65-trackers-forges-probes-and-actions)).

**Schedules.** A schedule opens a plan at the times a cron expression names, in a time zone,
on a branch resolved to its commit at each tick. It replaces CronJobs creating PipelineRuns
outside the queue: its pipelines go through the queue, its runs are read like any other. A tick
does not start while the schedule's previous run is still going, and a tick missed during an
outage is recorded as missed rather than run late.

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
2. A release is drafted from a git ref, emitting `release.drafted`. The issues the release
   brings are detected from the merged pull requests, and completed or corrected by hand;
   the reference can still change. A draft nobody will launch can be deleted, which frees its
   tag.
3. The draft is launched, emitting `release.created`: the reference is resolved to the commit
   it names at that instant, the git tag is set, and the project's declared plan starts:
   build, then the tests and audits it requires. A release produced from its source, a final,
   runs what its source built instead: each image re-tagged, what cannot be re-tagged built
   again under its own tag, then `ready` with no verification. A project that declares
   nothing to run releases a tag, `ready` at once. The reactions listening for
   `release.created` start here.
4. Each job is queued, dispatched to Tekton, and observed. Its results — test cases,
   security findings, benchmark measures, artifacts — are read back from the object
   store and the pipeline's own output.
5. The release note, assembled on the draft, is edited if needed and exported.
6. A release whose mandatory pipelines all concluded is `ready`; a releaser approves it to
   `released`, which makes it promotable. What happens next (promotion to an environment,
   sign-off, GitOps deployment) is a plugin's business.

## 1.4 Vocabulary

| Term | Meaning |
|---|---|
| **capability** | What a plugin brings: a pipeline, a tracker, a forge, probes, actions, or a module. |
| **kind** | A pipeline the catalogue can launch, named by the capability it binds (`docker/build`, `php/unit-tests`, `deploy/security-audit`, …). The core names none: a pipeline producing an image or binaries builds, and one declaring how it promotes, a Docker build, is re-tagged rather than rebuilt for a final. |
| **catalogue** | The YAML file that binds the plugins' pipelines to this instance: which Secret fills a need, which account deploys, timeouts, volumes. See [05](05-pipeline-catalogue.md). |
| **step** | One pipeline or action inside a plan stage (`gestes` in a stored plan). |
| **fact** | An event normalised to a type a reaction can match. |
| **module** | Loops, screens and routes an installed plugin's program runs; on while its plugin is on for a project. |

## 1.5 Documentation map

The chapters are listed in [the index](README.md).
