# 6. Plugins

Everything domain-specific lives in a plugin. The core manages projects, versions,
releases, jobs and results; it knows nothing about deployment environments, GitOps,
sign-offs, or any particular tracker.

## 6.1 The model

A plugin brings **capabilities**, each of a kind. The kind qualifies the capability, not the
plugin: one plugin may bring a pipeline and a screen.

| Kind | What it extends |
|---|---|
| `pipeline` | A Tekton pipeline, what it publishes, the privileges it needs, and how its runs are read |
| `tracker` | Where versions and issues come from |
| `forge` | Where code and refs come from, and where tags, statuses and comments go |
| `exporter` | What a release can be exported as |
| `module` | Screens, routes and loops the dashboard runs on the plugin's behalf |

A plugin declares itself in its own package, as a `capability.Plugin`: its settings, events and
actions, and what it brings.

```go
capability.Plugin{
    ID:         "gitea",
    Version:    "1.0.0",            // changes whenever interpretation changes
    APIVersion: sdk.APIVersion,     // the contract it was built against
    Label:      "Gitea",
    Summary:    "Branches, tags and pull requests…",
    Global:     []capability.Field{…},  // settings that belong to the instance
    Project:    []capability.Field{…},  // settings a project may override
    Events:     []capability.EventSpec{…},
    Actions:    []capability.ActionSpec{…},
    PerProject: true,                   // may be enabled per project
    Forge:      forge{},                // what it brings
    Probes:     []capability.Probe{pullProbe{}},
    Executor:   executor{},
}
```

The types a plugin compiles against form the **contract**, a Go module of its own:
`forge.cadoles.com/vcarroy/arkhitekton/sdk`, kept in this repository and replaced locally by
`go.mod`. It imports only the standard library:

| Package | What it holds |
|---|---|
| `sdk/capability` | the plugin declaration, pipelines, interpreters, tracker, forge, probe and action contracts |
| `sdk/parse` | the records the dashboard's parsers hand over |
| `sdk/facts` | what an interpreter returns |
| `sdk/interpret` | helpers shared by interpreters |
| `sdk/provider` | the vocabulary of trackers and forges: versions, issues, refs, pull requests |
| `sdk/declare` | the schema of a declared reading (§6.3) |
| `sdk/manifest` | the schema of a plugin an instance installs (§6.12) |
| `rpc/extension` | the program of a native plugin: its hooks served, the Host lent (§6.4) |

The dashboard refuses a plugin built against another major `sdk.APIVersion`. The built-in
plugins live under `internal/plugins/<id>` and import nothing but the contract; only
`internal/plugins/builtin` lists them, and only the `app` package assembles the registry the
rest of the core is given. Tests enforce both rules. The front-end has the same rule, checked
by a test: a single module file may import feature code.

## 6.2 Pipeline capabilities

A pipeline capability is declared by its plugin: the Tekton Pipeline and Tasks it ships, or the
ones it expects the operator to provide; its parameters; the artifacts it publishes and the
parser each file is read with; the privileges it needs; and how its runs are **read**: by an
interpreter written in Go, a reading declared in YAML (§6.3) or a native plugin's program
(§6.4), one way and only one. Capabilities are named
`plugin/name` (`php/unit-tests`), so two plugins cannot collide. The
[catalogue](05-pipeline-catalogue.md) binds them to an instance.

Reading a run starts when it is over. The dashboard fetches the artifacts the capability
declares, parses each file with a parser of its own (JUnit, mochawesome, PHPStan, PHP-CS-Fixer,
k6, the security report) or hands over the bytes (`raw`) for a format it does not know, and
passes the records with the run's state as Tekton reported it: each task's status and reason,
each step's exit code. The reading returns **facts** in a shape the dashboard defines:

| Shape | Identity | Compared by |
|---|---|---|
| `cases` | suite and name | change of status between releases |
| `findings` | fingerprint | appearance and disappearance, severity |
| `measures` | scenario and metric | distance to a pinned baseline |
| `attachments` | none | never compared, never scored: files shown as they are |

The set of shapes is closed, and only the core adds one: a shape is a schema, a comparison
and a screen. Any set may also carry **attachments**, the files kept with it, and free
**attributes** shown as they are. A file is kept by declaring its member with the `attach`
parser: the dashboard stores an archive's member as an object of its own, streamed rather than
loaded, and hands the interpreter where it is; an interpreter can only attach what the
dashboard kept for that run. The end-to-end capability keeps each spec's video and each
failure's screenshot, the latter shown under the case it illustrates.

Each stored result names the plugin version that read it, its shape and its group, so it stays
readable when the plugin changes or goes. A replay reads a run again with the same reading, and
refuses one another version read unless asked.

The built-in plugins are `core` (build, re-tag), `php` (static analysis, unit tests), `security` (the merged security audit), `k6` (load
campaigns, observed rather than launched), `redmine` (tracker), `gitea` (forge) and
`bon-de-livraison` (the delivery note).

## 6.3 Declared readings

A capability whose runs produce test cases can declare its reading instead of coding it:
`capability.Pipeline.Reading` carries YAML the dashboard executes, checked when the plugin is
loaded. `php/static-tests` and `php/unit-tests` are read this way
(`internal/plugins/*/readings/`). A pipeline reads its runs in code or by declaration, never
both.

```yaml
apiVersion: arkhitekton.reading/v1
tools:
  - tool: cs-fixer            # as the artifact members name it
    task: run-cs-fixer        # whose state tells "ran and left nothing" from "never started"
    group: static
    summary: { violations: "style deviation(s)" }   # or: tests
    empty: pass               # the verdict of a report with no case; absent, derived
    exitCodes: { results: [0, 8] }   # any other code: the tool did not conclude
    cases:
      - from: csfixer.fixers  # a view of the report, one row per fixer
        suite: "{file}"
        name: "{fixer | or: 'style'}"
        status: failed        # or { from: field, map: { value: status } }
        message: "style deviation: {fixer | or: 'style'}"
        trace: "{diff}"
```

A tool's report becomes a set of cases, one per row of each `cases` rule, in the order the
rules are declared. A task that ran and left no readable report did not conclude, and so did
one whose exit code `exitCodes` does not list, whatever its report says: the summary gives the
code and why the task ended (`the tool did not conclude (code 137): OOMKilled`). One that never
started says nothing. A tool may also read its `duration` from the report as a whole.

**Views** are what a declaration reads; the dashboard offers them and adds them:

| View | Parser | Fields |
|---|---|---|
| `junit.cases` | `junit-xml` | `path`, `classname`, `name`, `duration_ms`, `outcome` (`passed`, `failed`, `errored`, `skipped`, `rerun`), `detail`, `detail_type`, `trace` |
| `mocha.tests` | `mochawesome-json` | `file`, `suite`, `title`, `name`, `duration_ms`, `outcome` (`passed`, `failed`, `pending`, `rerun`, `not-run`), `retries`, `message`, `trace` |
| `phpstan.messages` | `phpstan-json` | `file`, `line`, `identifier`, `message`, `ignorable` |
| `phpstan.errors` | `phpstan-json` | `message` |
| `csfixer.fixers` | `cs-fixer-json` | `file`, `fixer` (empty for a file listed with none), `diff` |

A whole JUnit or mochawesome report offers `duration_ms` to a tool's `duration`.

**Templates** mix text and `{field | filter | ...}` expressions. The filters are closed:
`firstline`, `trim`, `lower`, `or: <field|'text'>` (the value, or another when it is empty) and
`replace: 'regex' 'replacement'`. There is no loop, no condition and no call: a need that does
not fit belongs to an interpreter in code.

Everything is checked when the plugin is loaded: the views exist and read what the pipeline's
artifacts parse for that tool, every field a template reads is one the view offers, every status
table covers its field's values. A declaration that does not hold stops the dashboard at
start-up rather than misreading a run.

**Checking a reading before shipping it.** `arkhitekton check-reading [--write] <reading.yaml>
<case>...` (or `--plugin <dir>` for a program's, §6.4) reads recorded cases without a database nor a cluster. A case is a directory with
`state.json` (the run's state), `records.json` (`[{"tool", "parser", "file"}]`, a tool without
a file having left no report) and `expected.json` (the facts); `--write` records the facts
produced where `expected.json` is missing. The built-in readings' cases live in
`app/testdata/readings/`.

## 6.4 Native plugins

What a declaration cannot say, a format the dashboard does not parse, a screen, a table, a loop
that reads the cluster: a plugin brings a **program**. The dashboard launches it as a separate
process, in its own pod, and talks to it over gRPC (`hashicorp/go-plugin`, the model of
Terraform, Vault and Grafana's backend plugins). The contract is
`rpc/proto/arkhitekton/plugin/v1/plugin.proto`, version 1 of the protocol; a program of another
version is refused at the handshake.

**What a program answers**, each declared in the manifest (§6.12):

| Hook | Called |
|---|---|
| reading a run (`interpreter: native`) | when a run of one of its pipelines concludes, or is replayed |
| its routes | for a request below its prefix, once the dashboard has checked it (§6.12) |
| `deployments`, `promotions` | by the release screen and the releases list |
| `environmentSuggestions` | by the launch form |
| `schemeCheck` | when a project's version scheme is saved |
| `promotionFacts` | when a promotion job concludes; one plugin at most states it |
| `projectSettings` | when a run is composed, for the configuration keys it adds |
| its module | under the worker's lease, while a project switches the plugin on (§6.9) |
| its tracker, its forge (`tracker: true`, `forge: true`) | as a compiled plugin's (§6.5) |
| its probes (`probes:`) | at the cadence the manifest declares, under the worker's lease |
| its actions (`actions:`) | when a plan carries one of the steps it declares |

**What the dashboard lends it** is the `Host` service: a project, a release, the queue, the audit
log, its own settings for a project (unsealed), the environments a project declares, and the
dashboard's authorisation. Its own tables it reaches with the database connection it is given.

**Writing one in Go.** `rpc/extension` serves the hooks and lends the Host. A tracker, a forge,
probes and an executor are the sdk's own interfaces, as a compiled plugin declares them:
`Hooks.Tracker`, `Hooks.Forge`, `Hooks.Probes` and `Hooks.Executor`.

```go
package main

import "forge.cadoles.com/vcarroy/arkhitekton/rpc/extension"

func main() {
    extension.Serve(func(ctx context.Context, runtime extension.Runtime) (extension.Hooks, error) {
        return extension.Hooks{
            Interpreters: map[string]capability.Interpreter{"unit-tests": reader{}},
            Routes:       routes(runtime.Host), // an http.Handler
        }, nil
    })
}
```

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/plugin-linux-amd64 .
```

A route's handler is plain `net/http`: `extension.IdentityFrom` and `extension.ProjectFrom` give
the caller and the project. The program is static (the dashboard's image is distroless) and
built for each platform it is published for.

`rpc/examples/gotest` is a complete example: it reads the event stream of `go test -json`, a test
per case, a package that does not build as a case of its own, a stream cut off as tests not run.

**How it runs.** The program is written from the installed artifact to the pod's `/tmp`, checked
against its digest before every launch, and launched at its first use. Its environment is
explicit: `DATABASE_URL`, and in the worker the cluster's address; not the dashboard's sealing
key, object store credentials, OIDC secret or forge and tracker tokens. A program that dies is
launched again after a few seconds; meanwhile its hooks are left out and its routes answer 503.
A version that leaves the capabilities in force is stopped. Each api replica runs its own
processes; a module runs only in the elected worker.

**Checking a reading.** `arkhitekton check-reading --plugin <dir>` launches the program built for
the machine and reads recorded cases with it (`app/testdata/readings/gotest/`).

**A tracker, a forge, probes and actions over the wire.** Every call carries its target: the
program connects for it, and keeps its clients by target. A provider's errors cross as themselves
(`provider.ErrNotConfigured`, `ErrWriteForbidden`, `ErrTagExists`), so that the dashboard branches
on them as on a compiled plugin's. The tracker's links are asked once per distinct settings, as
patterns the dashboard fills with each identifier. A message is 32 MiB at most either way, a call
the target's timeout and five seconds, a probe's pass two minutes.

## 6.5 Trackers, forges, probes and actions

A plugin that brings a tracker or a forge returns a client for a **target**: the project's
repository and the plugin's settings for that project, resolved and unsealed by the dashboard,
with a timeout the deployment sets. The plugin never sees the database, and never chooses whom
it talks to: the target comes from what an administrator typed. The deployment's `TRACKER_*`
and `FORGE_*` variables only seed those settings.

At most one enabled plugin brings the instance's tracker, and one its forge: the
administration screen refuses a second. An installed plugin that brings either is installed
switched off: the administrator switches it on once the other is off. Its manifest sets
`perProject`, since a project chooses its tracker and forge. A project with none falls back to what works without
it: the release form takes a free-text git reference, the versions screen shows no links.

A **probe** observes an outside source and reports observations under the suffixes its plugin
declares as events. The dashboard names them (`gitea` plus `pull_request.opened`), keeps the
cursor, deduplicates on the probe's key, isolates each project's failures and triggers nothing
on a first pass. The tracker synchronisation is the core's own probe, run once per plugin that
brings a tracker: it writes versions and issues to the core's tables.

An **executor** carries out the actions a plugin declares. The dashboard resolves the plugin's
settings, refuses when it is switched off for the project, interpolates the parameters and
checks them against the declaration; the executor only talks to its boundary.

## 6.6 Settings

A descriptor declares its settings as fields:

```go
capability.Field{
    Name: "kustomRepoUrl", Label: "Dépôt de kustomisation",
    Type: capability.FieldText, Help: "…", Secret: false,
}
```

Types are `text`, `url`, `bool` and `list`. A field marked `Secret` is sealed with
`SECRETS_KEY` before it reaches the database and is never returned by the API; without
that key, saving it is refused and the interface says so where it is typed.

**Two scopes.** `Global` fields are set once for the instance. `Project` fields may be
overridden per project. Resolution is *inheritance, not merge by value*: an empty
project value does not erase the instance value, it means "not overridden". Absence of a
row means inheritance, which is what makes a new project work without being configured.

Settings are edited under **Administration → Plugins** (instance) and in each project's
**Plugins** tab. Environment variables may *seed* them at first start-up, filling only
what is missing — see [04 §4.12](04-configuration.md#412-plugin-seeds).

## 6.7 Per-project activation

A descriptor with `PerProject: true` can be enabled or disabled project by project. A
plugin without it is instance-wide and does not appear in a project's plugin list.

Every built-in plugin can be switched off on the instance, except `core`, which the
dashboard cannot work without. A pipeline plugin switched off has its jobs refused with the
reason, and a delivery-note plugin switched off for a project offers no deliverable there.

The state resolves to: enabled here, disabled here, or inherited from the instance.
Asking for a disabled plugin's settings returns a sentinel error rather than empty
values, so that a caller cannot mistake "switched off" for "not configured".

Routes belonging to a module are gated: a request to a module's route, for a project
where it is disabled, is refused by middleware that resolves the project from the path.
The interface hides the screens the same way, from the same source of truth.

## 6.8 Events and actions

A plugin may declare **events** it emits and **actions** it offers. Both are named by a
suffix, which the core prefixes with the plugin id — a plugin `deploy`'s `release.promoted`
becomes the event type `deploy.release.promoted`. Prefixing is not decoration: two plugins may both have
something to say about "deployed".

An event declares the shape of its payload; an action declares the fields it takes, any
of which may be a template filled from the event that triggered it. Together they are what
makes a plugin reachable from the automation screen without the core knowing what it does.

## 6.9 Modules

Some plugins need loops that write the dashboard's tables and talk to the cluster, which a
contract plugin cannot do. Those are **modules**: an installed plugin's program runs one when its
manifest says `module: true` (§6.4, §6.12).

The worker's supervisor runs a module while its plugin is switched on for at least one
project, and restarts it with its new settings when they change. An installed plugin's module
starts once the plugin is installed, is replaced by the next version activated, and stops when
the plugin is uninstalled. Switching a module on or
off, or editing its settings, applies within thirty seconds, without a restart. A module that
fails is started again at the next tick; the worker keeps running.

A plugin that needs tables owns them. An installed plugin carries its own migrations and its own
version table (`goose_<id>_db_version` by convention), applied when it is installed, and the
dashboard adds its readiness check (`schema-<id>`). The core's schema does not mention the
plugin's tables, and dropping the plugin means dropping its migrations, not editing the core's
history.

## 6.10 Environments and deployment

The core knows a project's **environments**, declared per project in the catalogue (doc 05
§5.5) with their rank, namespace, visibility, observability and sensitivity, and the
`deploy:<environment>` permission they derive (doc 09 §9.3). It does not deploy anything
itself: re-tagging an image (`core/retag`) is the only promotion it can launch.

How a promotion reaches an environment, how the dashboard learns what an environment actually
runs, and who signs a release off are one way of deploying, brought by an installed plugin
(§6.4): its hooks tell the release screen which environments run a release, list its promotions,
suggest environments to a run, refuse a version scheme it cannot deploy, and state the fact of
a concluded promotion. An instance without such a plugin shows no deployment.

## 6.11 Writing a plugin

A plugin imports the `sdk` module, and for a program the `rpc` module, and nothing else of the
dashboard. Two ways to bring it:

- **built in**: added to `internal/plugins/builtin`, released with the dashboard;
- **installed** from a registry without recompiling, its pipelines and, through a program
  (§6.4), its routes, tables, hooks, module and screens (§6.12).

 Its version changes
whenever what its interpreters read changes: a replay refuses to read a run with another
version than the one that read it, unless asked.

The licensing is settled: an additional permission under section 7 of the AGPL lets a plugin
that uses only the plugin interface be published under any licence its author chooses, and the
combined work be distributed or served on those terms. See `LICENSE.plugin-exception`.

## 6.12 Installed plugins

An administrator installs a plugin **without recompiling**: an OCI artifact pinned by digest,
signed, pulled and verified by the worker, put in force without a restart. It brings pipelines
(Tekton definitions, what they publish and need, how their runs are read) and, through a program
(§6.4), routes, tables, extension hooks, a module and screens.

**Trust.** Installing is the administrator's decision, and its consequences are theirs, as in
Jenkins or Grafana: a plugin's program runs as the dashboard does, and its interface with the
user's session. What the dashboard guarantees is that what runs is exactly what was installed
(the digest, and a signature by a trusted key unless the operator names the plugin in
`PLUGIN_ALLOW_UNSIGNED`), that the installation is audited, and that what reaches the cluster stays
the operator's to grant (doc 09 §9.9).

**The directory.** `arkhitekton-plugin.yaml` at its root, the files it names next to it.
`rpc/examples/gotest` is a complete one, `app/testdata/providers` one that brings a tracker and a
forge; a plugin with everything:

```yaml
apiVersion: arkhitekton.plugin/v2
id: deploy
version: 2.0.0               # letters, digits, dots and dashes
sdk: "1"                     # the sdk.APIVersion it was built against
label: Deploy
perProject: true             # a project switches it on or off
settings: { global: [], project: [] }        # capability.Field
binary: bin/plugin           # its program, built per platform beside: bin/plugin-linux-amd64…
protocol: 1
migrations: { dir: migrations, table: goose_deploy_db_version }
events: [{ suffix: release.promoted, label: Promotion aboutie, payload: [] }]
module: true
hooks: [deployments, promotions, environmentSuggestions, schemeCheck, promotionFacts, projectSettings]
routes:
  - { method: GET, path: environments, permission: read }
  - { method: POST, path: "releases/{tag}/promote/{environment}", permission: "deploy:{environment}" }
  - { method: GET, path: status, permission: admin, scope: instance }
ui: { entry: ui/index.js, styles: [ui/index.css], resources: [ui/resources/*.yaml] }
tracker: true                # its program connects to a tracker, a forge (§6.5)
forge: true
probes: [{ name: pull_requests, interval: 5m, eager: false }]   # reports declared events
actions:
  - { suffix: status, label: Statut de commit, summary: Pose un statut.,
      fields: [{ name: commit, label: Commit, required: true, template: true }] }
pipelines:
  - name: unit-tests
    label: Tests
    pipelineRef: go-unit-tests
    tekton: [tekton/pipeline.yaml, tekton/task.yaml]
    externalTasks: [git-clone]
    params:
      repo-url: { source: binding, defaultKey: project.repoUrl }
      revision: { source: input, required: true, label: Révision }
    workspaces:
      - { name: git-credentials, need: source-read }
      - { name: policies, configMap: { reason: le barème de sévérité } }
    needs: [{ name: source-read, reason: clone the project }]
    artifacts:
      - { name: results, param: results-upload-url, object: go-test.json, format: file,
          members: [{ match: go-test.json, parser: raw, tool: gotest }] }
    produces: [results]
    group: unit
    interpreter: native        # or reading: readings/unit-tests.yaml
```

A manifest `arkhitekton.plugin/v1` is refused with a message: rebuild the plugin.

**Routes.** Served under `/api/v1/projects/{project}/plugins/<id>/` (scope `project`, the
default) or `/api/v1/plugins/<id>/` (scope `instance`). Before the program sees a request, the
dashboard authenticates the caller, checks the CSRF token of an unsafe method, checks that the
project switches the plugin on, and checks the route's permission: a role's (`read`,
`release:edit`, `admin`…) or `deploy:{param}`, judged as a promotion to the environment the path
names. The program receives the caller and the project, never the session; its answer keeps its
status, its body and its `Content-Type`, `Cache-Control`, `Content-Disposition` and `Location`.
A body is 4 MiB at most each way, an answer 30 seconds.

**Tables.** The plugin's SQL migrations (goose files) are applied when it is installed, under the
worker's lease, before the installation is recorded: whether a version is active is switched
without a deployment, and its tables must be there. They keep their own version table, which
`/readyz` checks as `schema-<id>`. Keep them compatible with the previous version: both can be
installed side by side. Uninstalling leaves the tables and their rows, until they are purged.

**Screens.** The plugin's screens, menu entries and blocks are resources, `ui.resources`
(§6.14). Its `ui.entry`, if it has components of its own, is a script built with the dashboard's
preset (`apps/web/plugin.vite.ts`) that calls `registerComponent` from `@arkhitekton/host` for
each `<id>/<name>` its resources render. It carries neither React nor the router: it takes the
dashboard's from `window.__arkhitekton`. `/me` lists the active plugins' scripts; the browser
loads them before its first screen, from `/api/v1/plugin-interfaces/<id>/<version>/`. A script
that fails is left out, and the console says so. Its stylesheet carries only its own utilities,
on the dashboard's theme (`src/styles/plugin.css`). A script without resources is the module
contract arkhitekton 0.4 retired: such a plugin is refused at installation and activation.

**What is refused**, when the plugin is published and again when it is installed:

| Refused | Why |
|---|---|
| a key the schema does not have, a service account first | a privilege is the operator's to grant, in the catalogue file |
| a product other than results | an image is the build's |
| a Tekton definition other than a `Pipeline` or a `Task`, or two in one file | the dashboard installs Pipelines and Tasks, and installs what it checked |
| `serviceAccountName`, `hostPath`, `hostNetwork`, `hostPID`, `hostIPC`, `hostUsers`, `privileged: true` anywhere in a definition | a run reaches nothing beyond its pod |
| a `resolver` or a `bundle` | a definition fetches nothing its signature does not cover |
| a Task the Pipeline runs that is neither shipped nor declared in `externalTasks` | |
| a route whose permission no role grants, a hook the dashboard does not know, a second plugin stating promotions | |
| a program of another protocol, a program missing for the platform | |
| a path leaving the directory, a link, more than 256 MiB in all | |
| the id of a built-in plugin, a Tekton definition another plugin ships | two plugins would overwrite each other |

**Publishing.** Build the program for each platform, then pack, push and sign:

```sh
make example-plugin     # builds rpc/examples/gotest/bin/plugin-linux-{amd64,arm64}
arkhitekton publish-plugin --dir rpc/examples/gotest \
  --ref registry.example/plugins/gotest:1.0.0 --key plugin-key.pem
# published  registry.example/plugins/gotest@sha256:…
# signed by  sha256:…
```

A plugin with a program is published as an index, one artifact per platform carrying the program
built for it at the declared path; the signature covers the index. The worker pulls its own
platform's: the dashboard's pods share one architecture. The key is an unencrypted PEM private
key, ECDSA or Ed25519; registry credentials are read from the Docker `config.json`,
`--plain-http` reaches a registry without TLS. The signature is cosign's "simple signing" format
(`sha256-<digest>.sig` next to the artifact), so `cosign sign --key … --tlog-upload=false` signs
an artifact the same way. A key pair without cosign:

```sh
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out plugin-key.pem
openssl pkey -in plugin-key.pem -pubout -out trusted-keys.pem
```

**Installing.** Administration, Plugins: the reference **by digest**, the one
`publish-plugin` printed. A tag can be moved after an administrator looked at what it named; a
digest cannot. The request becomes a `plugin-install` job: the worker pulls the artifact,
checks its signature against `PLUGIN_TRUSTED_KEYS` (§4.14) before fetching the layer, or lets it
in unsigned if `PLUGIN_ALLOW_UNSIGNED` names it, checks the plugin as above, checks that the
capabilities still build with it, applies its migrations, stores the layer under
`plugins/<id>/<version>/`, and records the key that signed it (or `unsigned`). A failure is read
on the installation, and on the job. Settings already recorded for the plugin's id are kept.

**A source the instance does not recognise.** Unsigned, or signed by a key no trusted key
matches, and named by neither list: the installation stops in the state `unrecognised` instead of
failing, and waits. The screen warns that the plugin does not come from a recognised source, and
accepting it records the plugin's **id**, after which its next version installs without asking
again. Withdrawing that acceptance applies to the next installation; versions already installed
stay.

The id is what is recorded, and the only thing there is to record: the signature format carries
no public key, so an unknown signer leaves no fingerprint to keep or to compare. To read the id,
the layer is fetched before the plugin is trusted, which is what `PLUGIN_ALLOW_UNSIGNED` already
did: it is read, bounded, and nothing of it runs.

Installing a plugin is the administrator's act, and they already pin the digest, which asserts
more about an artifact than a signature by an unknown key. An instance that would rather a
compromised administrator account could install nothing sets `PLUGIN_OFFER_UNRECOGNISED` to
`false`: the refusal is then final, and the layer of a plugin that could not be accepted is never
fetched.

**Workspaces.** A workspace has exactly one source: the secret bound to a need, a claim created
per run, an empty directory, or a **ConfigMap the operator owns**. The last is for data a plugin
must not carry, because it belongs to the operator and changes without publishing a plugin: a
severity scale, a set of rules, a policy. It is not a need, and that is deliberate. A need is a
privilege whose set is closed, so that the operator reads from the name alone what granting it
means; mounting a ConfigMap grants nothing, since the dashboard binds it as a volume and the run's
service account never reads it. The plugin declares the workspace with a `reason`, one sentence
saying what it expects there, and the operator binds a ConfigMap of their own (§5.4).

**Binding.** An installed capability is launched once bound, from Administration, Capacités:
secrets for its needs, ConfigMaps for the workspaces that expect them, configuration keys for its
parameters, volume sizes, a timeout, a ceiling. Never a service account: a pipeline that asks for `cluster-deploy` is bound by the
operator's catalogue file, or its runs are refused with the reason. The catalogue file's
binding, when it has one, wins and is shown read-only (§5.2).

**Versions.** The first version installed is active. A later one is installed beside it and
waits to be activated: an update is a decision, not a side effect of pulling. Activation is
refused if the capabilities would not build with it, a binding that names a parameter the new
version dropped for one. New runs start with the active version; a run is read by the version
that launched it, while that version is installed. Routes, hooks, the module and the interface
are the active version's.

**Uninstalling.** Removing an inactive version removes that version. Removing the active one
uninstalls the plugin: every version, its bindings and its settings; its program is stopped. The
worker then withdraws the Tekton definitions it installed for it (those carrying
`arkhitekton/plugin=<id>`); a run already started keeps going on the copy Tekton made. The facts
its runs produced, and its tables, stay. A plan naming one of its capabilities fails at launch,
naming it.

**Purging.** Administration, Plugins, *Données conservées*: the tables an uninstalled plugin left,
by the version that was active. Its layer is kept for that, past the daily purge of unused
objects. Purging, confirmed by typing the plugin's id, is a `plugin-purge` job: the worker undoes
every migration with that layer's files, under the lease, then drops the version table and forgets
the cursors of the plugin's probes. It is refused while an installation is pending, and before
anything is undone when an applied migration has no `Down` section, which goose would run empty.
Only the plugin's tables go: the facts of its runs, the jobs and the audit are the core's.
Installing the plugin again before a purge finds its tables and rows.

**Taking effect.** Installing, activating, removing and binding move a counter the api and
the worker watch: each rebuilds its capabilities within thirty seconds, at once on the replica
that took the request. The worker starts and stops installed plugins' probes with them. Capabilities that do not build leave the previous ones in force, and are
tried again. A new interface appears when the page is loaded again.

## 6.13 Testing a plugin

`host/hosttest` gives an installed plugin's tests what the dashboard does, so that what passes
there runs the same once installed:

- `EmptyDatabase` (with `TEST_DATABASE_URL`), `MigrateCore`, and `Migrations{Plugin, Table,
  Files}` applying the plugin's own as the dashboard applies them at installation, rolled back
  or listed as pending;
- `Catalogue`: a catalogue file loaded as an instance with the plugin installed reads it;
- `Record`, `Interpret`, `CheckReading`, `SameFacts`: a report parsed by the dashboard's parsers,
  a run read by the plugin's interpreter, a declared reading checked against recorded cases.

A tracker, a forge, a probe or an executor is the sdk's interface: tested in-process against a
fake server, as `internal/plugins/gitea` and `internal/plugins/redmine` are. `rpc/providers`
carries them over the wire, and its tests show the program's side and the dashboard's together.

A route's handler is tested as plain `net/http`, its caller and project set with
`extension.WithIdentity` and `extension.WithProject`. A program as a whole is checked with
`arkhitekton check-reading --plugin`, its interface resources with `arkhitekton check-ui --plugin`
(or `hosttest.Interface`, which returns the composed interface for assertions), and its
components with the dashboard's own front-end checks, once dropped in `apps/web/src/plugins/<id>/`
of a tree built outside any ignored path.

The plugin requires the versions of `sdk` and `rpc` it is built against, and of the dashboard for
`hosttest`. Until arkhitekton 1.0, a minor version may change the kit, and says so in its notes.

## 6.14 Screens as data

The dashboard's screens, its main menu and the tabs of its screens are resources, in the manner of
Kubernetes objects (`apiVersion: ui.arkhitekton/v1`, schema in `sdk/ui`). The core's are in
`internal/ui/resources`; a plugin brings its own in `ui.resources`. The dashboard composes them,
filters them for whoever asks in the project they look at, and serves the result on
`GET /api/v1/ui`; the browser builds its router and menus from it, merging nothing.

**Three kinds.**

- `Screen`: a route (`path`, relative to its `parent`; none, the parent's index; `redirect`, an
  address kept for shared links), the data it loads, and its `blocks`, in order. Only the shell has
  no parent.
- `Navigation`: the main menu (`main`, whose `home` is where `/` leads) and the tabs a screen
  shows (`executions`, `admin`, `project`). An entry's `to` may cite the route (`{route.slug}`).
- `Widget`: a widget composed of others, with parameters, that blocks reuse with `use`.

A block is a component or a widget. A component is named `<owner>/<name>`: `core/…` are the
dashboard's, `<id>/…` those the plugin's script registers. A component may declare `slots`, places
inside it where others add blocks: the release header's and the release list's `badges`, the
artifacts' `rows`. A slot entry receives what the component passes it, the release of a row for
instance, first in what its templates read.

```yaml
apiVersion: ui.arkhitekton/v1
kind: Screen
metadata: { name: release }          # no prefix: a contribution to the core's screen
spec:
  blocks:
    - id: mse.deployment
      after: core.artifacts
      data: { deployment: { source: plugin, path: "releases/{route.tag}/deployment" } }
      widget:
        card:
          title: Déploiement
          children:
            - table:
                rows: deployment.environments
                empty: Aucun environnement
                columns:
                  - { label: Environnement, value: "{label}" }
                  - { label: Depuis, value: "{deployed.since | age}" }
                  - label: État
                    badge: { text: "{state}", tone: { from: state, map: { ok: success, drift: warning }, default: neutral } }
                  - label: ""
                    when: [{ field: can_deploy, equals: true }]
                    action:
                      label: Promouvoir
                      method: POST
                      path: "releases/{route.tag}/promote/{id}"
                      confirm: { text: "Promouvoir {route.tag} vers {label} ?", challenge: "{id}" }
                      invalidates: [deployment]
    - id: core.header
      slots: { badges: [{ id: mse.deployed, component: mse/deployed-badges }] }
---
apiVersion: ui.arkhitekton/v1
kind: Navigation
metadata: { name: main }
spec:
  home: /environments
  items:
    - { id: mse.environments, before: core.versions, label: Environnements, icon: server, to: /environments }
```

**Merging.** Resources merge by name, the way Kubernetes merges an object two managers apply:

- a plugin creates resources, blocks, entries and slot entries under its prefix (`<id>.`); a
  resource without it is a contribution to the core's resource of that name, refused if there is
  none;
- lists (`blocks`, `items`, a slot's entries) merge by `id`; an added entry goes `after` or
  `before` an anchor, the core's or the plugin's own, never another plugin's, so that what one
  plugin brings does not depend on another being installed; without either, it goes last;
- on an entry it does not own, a plugin may only add to its slots: it never removes, hides,
  replaces nor moves a block of the core or of another plugin. A field two sources set is a
  conflict (the main menu's `home`, for instance), refused naming its first owner.

The composition is checked when a plugin is installed and when a version is activated: a
contribution to a screen that does not exist, a creation without the prefix, a second owner, an
anchor that is not there, a component of another plugin, a route the plugin does not declare are
refused there with their reason, not discovered on a screen.

**Data.** A screen or a block loads named sources. `source: plugin` calls a GET route the plugin
declares, its path a template over the route (`{route.tag}`); `source: core` names one of the
core's reads (`release`, `releases`, `version`, `run`), shared with the screens that read it.
`when` shows a block, a widget or a table column only if every condition holds: `equals`, `in`, or
`present`. A block's `permission`, and whether its plugin is switched on for the project, are
the server's to decide; a permission that depends on the row, `deploy:{id}`, is the data's to
tell, through `when`. Hiding is not refusing: the API still decides.

**Widgets**, a closed vocabulary only the core extends: `stack` (a column, or a `row`), `grid`,
`card` (`collapsible`), `heading`, `keyValue`, `table` (columns with a `value` and its `link`, a
`badge`, or an `action`), `list`, `badge` and `stat` (tone `neutral`, `info`, `success`,
`warning`, `danger`, fixed or read through a table), `text`, `markdown` (paragraphs, lists,
emphasis, code and links, no HTML), `link` (a path of the dashboard or an https address),
`action`, `empty`, `use`, `tabs` and `outlet`. An `action` calls a route of the block's plugin
(POST, PUT or DELETE), after a confirmation (`confirm`, with a `challenge` to type back) and a
form (`form`: text, textarea, boolean or select fields, sent as a JSON object), then loads again
the data it `invalidates`. The dashboard fills in its route's permission and hides it from a role
that may not call it. A need the vocabulary does not cover is a component the plugin registers.

**Templates.** Text a widget shows is a template, `{path | filter | …}`, the grammar of declared
readings (§6.3) with dotted paths. Inside a table or a list, a row's fields are read first, then
the block's data, then the route (`route`). Filters: `firstline`, `trim`, `lower`, `upper`,
`or: <path|'text'>`, `date`, `datetime`, `age`, `bytes`, `count`, `join: 'sep'`. The dashboard
checks them at installation; the browser renders them. A number is written as is, a boolean
`oui` or `non`, a list joined, an object not at all.

**A block that fails** shows where it stands, with its id, and the rest of the screen renders: a
plugin in trouble does not take the core's screen with it.

**Facts' labels** come from the manifest's `events`, served with the interface, read even while
the plugin is switched off.

