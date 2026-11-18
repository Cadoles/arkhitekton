# 5. Pipeline catalogue

Pipelines come from **plugins**. A plugin brings *capabilities*: a capability is a Tekton
Pipeline and its Tasks, the parameters it takes, what it publishes, the privileges it needs and
how its runs are read. The catalogue says **how this instance runs them**: which configuration
key fills a parameter, which secret satisfies a need, which service account deploys, how long a
run may last and how large its volumes are. It never describes a pipeline, and a plugin never
chooses a privilege.

Location: `PIPELINE_CATALOG_PATH`, mounted from a ConfigMap
(`deploy/base/config/catalog.yaml` in the reference deployment).

Decoding is **strict**: an unknown field is an error, and an unparseable catalogue stops
start-up. A typo that silently disabled a parameter would surface as a pipeline
misbehaving in production.

## 5.1 Top level

```yaml
maxConcurrentRuns: 10        # instance-wide ceiling on running pipelines; default 10, at least 1
capabilities: []             # see 5.2
projects: {}                 # see 5.5
podTemplate: {}              # optional, the pull secrets of every pipeline pod
```

There is no other way to declare a pipeline: a pipeline enters the dashboard only through a
plugin. A catalogue that binds no available capability stops start-up. The former `score:` key is
refused. A pipeline's weight in the score is not part of its binding: it is set from the
Administration screen's capabilities tab, or under `scoreWeights` in the versioned configuration
(doc 10), and defaults to its nature's (§5.8).

`podTemplate` is `{ imagePullSecrets: [name] }` or `{ imagePullSecretsConfigKey:
pipeline.imagePullSecret }`. An entry's own `podTemplate` replaces it entirely, with no merge; a
config key that resolves to nothing fails the run when it is created.

## 5.2 Binding a capability

```yaml
- ref: php/unit-tests       # plugin/capability
  verification: true         # (optional) overrides the capability's default
  maxConcurrent: 1           # (optional) default 1 for an exclusive capability, 5 otherwise;
                             # a lock the capability declares (doc 06) applies on top
  timeout: 45m               # (optional) a Go duration, Tekton's default when absent; an invalid one fails the run, not start-up
  params: {}                 # see 5.3
  needs: {}                  # see 5.4
  volumes: {}                # see 5.4
  configMaps: {}             # see 5.4
  podTemplate: {}            # (optional) overrides the top-level one
```

The order of the entries is the order in which a build chains its verifications.

The built-in plugins bring `docker/build`, `docker/retag`, `docker/scan`, `php/static-tests`,
`php/unit-tests`,
`go/build`, `go/unit-tests`, `go/static-tests`, `go/vulncheck`, `k6/load` (observed, not
launched) and `review/pull-request`; an installed plugin brings its own. Their Tekton definitions are installed by the dashboard when the capability
ships them, or provided by the operator otherwise.

A binding naming a plugin that is not installed waits for it, and says so at start-up: the file
binds what only it can bind, a service account, before the plugin is installed. A binding naming
a capability its plugin does not bring, or a parameter, a need or a volume the capability does
not declare, stops start-up.

**Bindings recorded from the screen.** An administrator binds, from the Administration screen's
capabilities tab (*Capacités*), what the file does not: the capabilities of a plugin installed
from a registry first (doc 06 §6.12). A recorded binding has the shape of an entry above, but a
`serviceAccountName` under a need is refused: a capability that needs `cluster-deploy` is bound
by the file only. **The file
wins**: a capability it binds is shown read-only, and its recorded binding, if any, ignored. A
recorded binding that no longer holds does not stop start-up: the dashboard starts on the file
alone, says so, and keeps trying.

## 5.3 Parameters

A capability declares every Tekton parameter it takes, and where its value comes from:

| Source | Meaning |
|---|---|
| `binding` | Resolved from a configuration key: the capability's default key, or the one the catalogue binds |
| `input` | Provided when the run is launched, by a human or by an automation |
| `literal` | A fixed value |

A `binding` parameter declared `overridable: true` takes a step's value when the step supplies a
non-empty one, and its key's otherwise: an integration test deploying the branch a pull request
names, for instance. Only such a parameter: a repository, a secret's name or a literal value never
come from a step.

The catalogue only rebinds `binding` parameters to another key:

```yaml
params:
  repo-url: { key: project.repoUrl }
```

An `input` carries form metadata (`label`, `help`, `placeholder`) and a `suggest` that names
the *nature* of the value so the launch form can offer a list: one of `git-ref`, `release-tag`,
`environment`, `image`, `image-name`, `build-target`, `pull-request`. With `suggest: image`, the `image` field
names the short repository the parameter expects; no other parameter takes that field, and a
`binding` parameter takes no `suggest`. `suggest` is also what lets an automation fill a
parameter with nobody typing: in a release, a `git-ref` or `release-tag` parameter is filled by
the run and a step cannot change it, and an `image` one is filled only when the step names none. A
`pull-request` parameter is filled with the request's number by a run on a pull request, launched
from its page or by a reaction to one of its facts, and a step cannot change it either; a
release's run gives none, so a step needing one is refused there. The launch form offers the open
pull requests.

**Config keys** available to `binding` parameters:

| Key | Value |
|---|---|
| `project.repoUrl` | Clone URL of the job's project, built from its forge and repository |
| `project.forgeUrl` | Public address of the job's project's forge, for a pipeline that reads its API |
| `project.reviewSecret` | Name of the project's Secret carrying the AI review's model key, written by the worker from the `review` plugin's sealed setting right before a run that binds it |
| `registry.project` | Registry repository prefix |
| `registry.insecure` | `true` when that registry is served over plain HTTP (`PIPELINE_INSECURE_REGISTRY`) |
| `project.deployKeySecret` | Name of the secret holding the project's own deploy key: it opens the project's repository and nothing else |
| `pipeline.gitSecret` | Name of the secret holding the deployment's git credentials, for a repository that is not the project's |
| `pipeline.registrySecret` | Name of the secret holding registry credentials |
| `pipeline.imagePullSecret` | Name of the pull secret |
| `minio.host`, `minio.pipelineBucket` | Object store as pipelines reach it |
| `forge.tekton.repoUrl` | Repository of the pipelines' definitions (`FORGE_TEKTON_REPO_URL`), for a pipeline that clones it; none shipped does |
| `tekton.namespace` | Namespace the PipelineRuns are created in |

A built-in plugin's settings are keys too, `<plugin>.<field>`: the instance's value with the
project's over it, every declared field given, an empty one included, and a sensitive one never.
An installed plugin adds its own keys, resolved per project, and documents them. A `binding`
parameter whose key is absent, or empty when the parameter is required, fails the run when the
scheduler creates it, naming the key, rather than starting a run that would act somewhere else.
One bound to no key at all makes the capability unavailable.

## 5.4 Needs, volumes, config maps and uploads

A capability declares the privileges it needs, from a closed set:

| Need | Satisfied by |
|---|---|
| `source-read` | a secret holding git credentials |
| `registry-pull` | a secret holding registry credentials |
| `registry-push` | a secret holding registry credentials |
| `cluster-deploy` | a service account |
| `model-access` | a secret holding a model provider's endpoints and keys |
| `forge-read` | a secret holding a forge token that reads pull requests and issues, never one that writes |

```yaml
needs:
  cluster-deploy: { serviceAccountName: tekton-kubectl }
  source-read:    { secret: { configKey: project.deployKeySecret } }
volumes:
  shared-data: { storageClassName: standard, storage: 2Gi }
configMaps:
  policies: { configKey: security.policies }
```

A secret is named directly (`secretName`) or through a config key (`configKey`). A pipeline
cloning the project's repository binds `project.deployKeySecret`, and a project with no active
key does not run it; a pipeline cloning another repository binds `pipeline.gitSecret`, and so
does a Go pipeline whose module requires private modules of the forge: a deploy key opens one
repository, and `go mod download` fetches several (§6.2). Only
`cluster-deploy` accepts a service account: it is the one need that is a cluster identity.
Volumes are per-run claims: the capability gives their default size, the catalogue their
`storageClassName`, and optionally another `storage` and `accessModes` (default
`ReadWriteOnce`).

Leaving a need unbound has two outcomes, and the difference is the workspace. `cluster-deploy`
carries none: the capability stays known, a plan may name it, and its runs are refused with the
reason. A need backing a workspace leaves that workspace with no source, which **stops start-up**
naming it: a PipelineRun whose workspace is bound to nothing is accepted by Tekton and fails at
mount time, where the reason is harder to read.

A **config map** is the fourth source a workspace may have, beside a secret, a per-run claim and
an empty directory. It carries data the operator owns and the pipeline reads: a severity scale, a
set of rules, a policy. It is not a need, and the distinction is deliberate. A need is a privilege,
and its set is closed so that the operator reads from the name alone what granting it means;
mounting a config map grants nothing, since the dashboard binds it as a volume and the run's
service account never reads it. Adding "a policy" to the set of privileges would make that set
grow with every kind of data a pipeline reads.

It exists for what a plugin must not carry: data that belongs to the operator and changes without
publishing a plugin. The capability declares the workspace and says, in one sentence, what it
expects there; the catalogue names the ConfigMap, directly (`name`) or through a config key
(`configKey`), for the same reason a secret may be named either way. Unbound, it stops start-up
like the workspace of an unbound need.

A pipeline never receives object-store credentials. For each artifact the capability
declares, the dashboard mints a **presigned URL** for one exact object under the run's prefix
and passes it in the parameter the capability names.

## 5.5 Per-project declarations

Sections under `projects:` are keyed by **project slug** and carry what a project
declares to a plugin — today, its environments:

```yaml
projects:
  my-project:
    environments:
      - id: staging
        label: Staging
        rank: 1
        namespace: app-staging
        deployer: { promotionSuffix: stg }   # free-form, read by the deployer
        observable: true       # can the dashboard see this cluster?
        visible: true          # shown in the interface
        sensitive: true        # promotion requires the releaser role
```

Ids and ranks must be unique **within a project**: two projects may each have a `staging`
of rank 1. An omitted `rank` is 0, so give every environment one. Absent, `observable`,
`visible` and `sensitive` are true; `deployer` maps strings to strings. A project absent from
this section simply declares no environment.

## 5.6 How a run is read

A run is read once it is over, every task included, by the **interpreter** of the capability
that launched it. The dashboard fetches the artifacts the capability declares, parses each file
with the parser the capability names (JUnit XML, mochawesome JSON, PHPStan or PHP-CS-Fixer JSON,
`go test -json`, a k6 summary, the security report, `attach` for a file kept as it is, or `raw`
for a format the plugin parses itself), and hands the
records to the interpreter together with the run's state as Tekton reported it: each task's
status and reason, each step's exit code.

The interpreter returns **facts** in a shape the dashboard defines: test cases, findings or
measures. Each stored result carries the plugin version that read it, so a replay reads it the
same way, and so it stays readable when the plugin changes or goes.

**What a pipeline leaves behind** is its capability's `produces`: `image` (the build's images,
whose digests the release records), `results` (reports read into facts), `binary` (files to
download, kept with the release) or `none`. A `binary` pipeline keeps its files as the
attachments of a result in the `build` group, never scored nor compared: the release's screen
lists them under **Artefacts**, each with its size and a download link. A plan's build stage
accepts an `image` or a `binary` pipeline.

**The run's state tells a crashed tool from a clean one.** Both leave nothing to count. A task
whose steps terminated ran; if it left no readable report, it did not conclude, and the result
says so instead of counting zero. A task that never started was not measured. Tekton keeps that
state even for a container killed before it could write a line, which no message on stdout
could report.

## 5.7 What a pipeline owes

1. Accept the parameters its capability declares.
2. Upload its reports to the presigned URLs it received, in the declared format.
3. Let each tool's exit code through, rather than swallowing it to keep the run green.

Nothing else. A pipeline that honours this contract is usable by hand, outside
Arkhitekton, which is deliberate: runs created outside the dashboard are observed rather
than driven, and remain readable.

## 5.8 Results and the score

A release's or a pull request's results show **one tile per pipeline launched** on it, by hand
(*Lancer un pipeline*, on either page) or by an automation, in the order they were first launched.
A pipeline launched again keeps its tile, which shows its latest result. Nothing is announced
before it runs: with nothing launched, the section is not shown. Results an outside runner left
(a k6 campaign) take a tile too. A build, a re-tag and the AI review, which has its own section,
take none.

Each tile weighs, in that order:

1. the weight an administrator set for its pipeline, between 0 and 100;
2. otherwise its nature's: unit tests 25, integration 20, end-to-end 20, static analysis 10,
   security 15, load 10;
3. otherwise nothing.

A tile weighing nothing is **out of the score**: measured and shown, it counts for nothing. An
accessibility audit is, until it is given a weight. The score is the weighted mean of the tiles
measured, the weights renormalised over them. A tile launched but not yet measured is named
beside the score, which is computed without it.

The weights are those in force when the score is read: changing one changes the score of every
release. A plan step's `weight` is still read, so that plans recorded before stay valid, but no
longer counts.
