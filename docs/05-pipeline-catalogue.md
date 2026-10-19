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
maxConcurrentRuns: 10        # instance-wide ceiling on running pipelines
capabilities: []             # see 5.2
projects: {}                 # see 5.5
score: {}                    # optional weights, per kind, for the release score
podTemplate: {}              # optional defaults applied to every pipeline pod
```

The legacy `pipelines:` section, which declared a pipeline whole, is still read alongside
`capabilities:` and will be removed.

## 5.2 Binding a capability

```yaml
- ref: mse/integration-tests # plugin/capability
  verification: true         # (optional) overrides the capability's default
  maxConcurrent: 1           # (optional) overrides the capability's default ceiling
  timeout: 45m               # (optional)
  params: {}                 # see 5.3
  needs: {}                  # see 5.4
  volumes: {}                # see 5.4
  podTemplate: {}            # (optional) overrides the top-level one
```

The order of the entries is the order in which a build chains its verifications.

The built-in plugins bring `core/build`, `core/retag`, `php/static-tests`, `php/unit-tests`,
`mse/integration-tests`, `mse/e2e-tests`, `security/security-audit` and `k6/load` (observed,
not launched). Their Tekton definitions are installed by the dashboard when the capability
ships them, or provided by the operator otherwise.

A binding naming a capability no plugin brings, or a parameter, a need or a volume the
capability does not declare, stops start-up.

## 5.3 Parameters

A capability declares every Tekton parameter it takes, and where its value comes from:

| Source | Meaning |
|---|---|
| `binding` | Resolved from a configuration key: the capability's default key, or the one the catalogue binds |
| `input` | Provided when the run is launched, by a human or by an automation |
| `literal` | A fixed value |

The catalogue only rebinds `binding` parameters to another key:

```yaml
params:
  mse-repo: { key: forge.mse.repoUrl }
```

An `input` carries form metadata (`label`, `help`, `placeholder`) and a `suggest` that names
the *nature* of the value so the launch form can offer a list: `git-ref`, `image`, … With
`suggest: image`, `image` names the short repository the parameter expects. `suggest` is also
what lets an automation fill a parameter with nobody typing. It never constrains execution.

**Config keys** available to `binding` parameters:

| Key | Value |
|---|---|
| `project.repoUrl` | Clone URL of the job's project, built from its forge and repository |
| `registry.project` | Registry repository prefix |
| `pipeline.gitSecret` | Name of the secret holding git credentials |
| `pipeline.registrySecret` | Name of the secret holding registry credentials |
| `pipeline.imagePullSecret` | Name of the pull secret |
| `minio.host`, `minio.pipelineBucket` | Object store as pipelines reach it |
| `forge.status` | Forge base URL used to report commit status |

A plugin adds its own keys, resolved per project: the MSE plugin contributes
`forge.mseKustom.repoUrl`, `forge.mseKustom.revision`, `forge.e2e.repoUrl` and
`mse.overlayLocation`. A required parameter that nothing resolves makes the enqueue fail,
naming the key, rather than starting a run that would act somewhere else.

## 5.4 Needs, volumes and uploads

A capability declares the privileges it needs, from a closed set:

| Need | Satisfied by |
|---|---|
| `source-read` | a secret holding git credentials |
| `registry-pull` | a secret holding registry credentials |
| `registry-push` | a secret holding registry credentials |
| `cluster-deploy` | a service account |

```yaml
needs:
  cluster-deploy: { serviceAccountName: tekton-kubectl }
  source-read:    { secret: { configKey: pipeline.gitSecret } }
volumes:
  mse: { storageClassName: standard, storage: 2Gi }
```

A secret is named directly (`secretName`) or through a config key (`configKey`). Only
`cluster-deploy` accepts a service account: it is the one need that is a cluster identity.
A need left unbound does not stop start-up: the capability stays known, a plan may name it,
and its runs are refused with the reason. Volumes are per-run claims; the capability gives
their default size, the catalogue their storage class and, if needed, another size.

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
of rank 1. A project absent from this section simply declares no environment.

## 5.6 How a run is read

A run is read once it is over, every task included, by the **interpreter** of the capability
that launched it. The dashboard fetches the artifacts the capability declares, parses each file
with the parser the capability names (JUnit XML, mochawesome JSON, PHPStan or PHP-CS-Fixer JSON,
a k6 summary, the security report, or `raw` for a format the plugin parses itself), and hands the
records to the interpreter together with the run's state as Tekton reported it: each task's
status and reason, each step's exit code.

The interpreter returns **facts** in a shape the dashboard defines: test cases, findings or
measures. Each stored result carries the plugin version that read it, so a replay reads it the
same way, and so it stays readable when the plugin changes or goes.

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
