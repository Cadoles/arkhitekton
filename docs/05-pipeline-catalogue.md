# 5. Pipeline catalogue

The catalogue is the only file Arkhitekton reads besides its environment. It declares
**which pipelines exist, what each one needs, and what it produces**. The core knows no
pipeline by name: a deployment that runs Go tests and one that runs PHP tests differ by
their catalogue, not by their build.

Location: `PIPELINE_CATALOG_PATH`, mounted from a ConfigMap
(`deploy/base/config/catalog.yaml` in the reference deployment).

Decoding is **strict**: an unknown field is an error, and an unparseable catalogue stops
start-up. A typo that silently disabled a parameter would surface as a pipeline
misbehaving in production.

## 5.1 Top level

```yaml
maxConcurrentRuns: 10        # instance-wide ceiling on running pipelines
pipelines: []                # see 5.2
projects: {}                 # see 5.5
score: {}                    # optional weights, per kind, for the release score
podTemplate: {}              # optional defaults applied to every pipeline pod
```

## 5.2 A pipeline

```yaml
- kind: unit-tests           # the class this pipeline implements
  pipelineRef: php-unit-tests# the Tekton Pipeline to run
  label: Unit tests          # what the interface shows
  verification: true         # this kind verifies a release
  produces: [results]        # image | results | none
  maxConcurrent: 2           # ceiling for this kind (optional)
  timeout: 90m               # (optional)
  serviceAccountName: tekton-kubectl   # (optional)
  params: {}                 # see 5.3
  uploads: {}                # see 5.4
  workspaces: []             # see 5.4
  podTemplate: {}            # (optional) overrides the top-level one
```

`kind` is free-form. `build`, `unit-tests`, `static-tests`, `integration-tests`,
`e2e-tests`, `security-audit` and `retag` are what the reference catalogue declares; a
deployment may declare others and the interface follows.

`produces` tells the core what to do when the run concludes: `image` means read the
digests it published, `results` means read the reports it uploaded, `none` means nothing
to collect.

## 5.3 Parameters

Every Tekton parameter a pipeline needs is declared, with where its value comes from:

```yaml
params:
  repo-url:  { source: config,  key: project.repoUrl }
  revision:  { source: input,   required: true, suggest: git-ref }
  overlay:   { source: literal, value: overlays/pipeline }
```

| Source | Meaning |
|---|---|
| `config` | Resolved from instance configuration, project settings and plugin settings, by `key` |
| `input` | Provided when the run is launched, by a human or by an automation |
| `literal` | A fixed `value` |

An `input` may carry form metadata — `label`, `help`, `placeholder` — and a `suggest`
that names the *nature* of the value so the launch form can offer a list instead of an
empty field: `git-ref`, `image`, … With `suggest: image`, `image` names the short
repository the parameter expects.

`suggest` is also what lets an automation fill a parameter with nobody typing: it says
that `app-revision` wants a git ref without the core having to know that name. It never
constrains execution — a suggestion is an aid, not a validation.

**Config keys** available to `source: config`:

| Key | Value |
|---|---|
| `project.repoUrl` | Clone URL of the job's project, built from its forge and repository |
| `registry.project` | Registry repository prefix |
| `pipeline.gitSecret` | Name of the secret holding git credentials |
| `pipeline.registrySecret` | Name of the secret holding registry credentials |
| `pipeline.imagePullSecret` | Name of the pull secret |
| `minio.host`, `minio.pipelineBucket` | Object store as pipelines reach it |
| `forge.status` | Forge base URL used to report commit status |

A plugin adds its own keys, resolved per project — the MSE plugin contributes
`forge.mseKustom.repoUrl`, `forge.mseKustom.revision`, `forge.e2e.repoUrl` and
`mse.overlayLocation`. A required parameter that nothing resolves makes the enqueue fail,
naming the key, rather than starting a run that would act somewhere else.

## 5.4 Uploads and workspaces

A pipeline never receives object-store credentials. For each upload declared, the
dashboard mints a **presigned URL** for one exact object and passes it as a parameter:

```yaml
uploads:
  results:
    param: results-upload-url   # parameter that receives the presigned URL
    artifact: unit-tests.tar.gz # object name under the release's prefix
    family: unit                # result family the report belongs to
    format: archive             # archive | json | …
```

Workspaces are declared the same way, and may be backed by a secret named through a
config key, by a volume claim template, or by an `emptyDir`:

```yaml
workspaces:
  - name: git-credentials
    secret: { configKey: pipeline.gitSecret }
  - name: shared-data
    volumeClaimTemplate:
      accessModes: [ReadWriteOnce]
      storageClassName: standard
      storage: 1Gi
```

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
of rank 1. A project absent from this section simply declares no environment.

## 5.6 The result protocol

Two channels, for two sizes of information.

**Markers, on stdout, during the run.** A pipeline writes single lines that the log
collector recognises. `MARKER_TOKEN` prefixes them: token `MSE` gives `##MSE-META##`,
empty gives `##META##`.

    ##META## {"tool":"phpstan","version":"1.10.5","exit_code":1}
    ##SUMMARY## {"status":"failed","error":"12 violations"}

They exist so a forty-minute campaign reports "PHPStan: 12 violations" as soon as PHPStan
finishes, instead of at the end. They carry kilobytes, never more: container logs are
rotated by the kubelet and are not a reliable transport.

**The descriptor is produced by the pipeline, never inferred.** Only the pipeline can
tell "the tool failed" from "the tool found nothing" — both yield zero violations, and
confusing them makes a crashed scanner look like clean code.

**Artifacts, through presigned URLs, at the end.** Full reports — JUnit XML, mochawesome
JSON, SARIF-like findings, benchmark measures — are uploaded to the URLs declared in
`uploads`. The dashboard reads them back and turns them into test cases, findings and
measures.

## 5.7 What a pipeline owes

1. Accept the parameters the catalogue declares for its kind.
2. Emit `##META##` when a tool starts and `##SUMMARY##` when it concludes.
3. Upload its reports to the presigned URLs it received, in the declared format.
4. Exit non-zero when what it verifies failed.

Nothing else. A pipeline that honours this contract is usable by hand, outside
Arkhitekton, which is deliberate: runs created outside the dashboard are observed rather
than driven, and remain readable.
