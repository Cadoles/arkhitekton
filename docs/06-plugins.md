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
| `sdk/guest` | the plugin side of a WebAssembly interpreter (§6.4) |
| `sdk/manifest` | the schema of a plugin an instance installs (§6.12) |

The dashboard refuses a plugin built against another major `sdk.APIVersion`. The built-in
plugins live under `internal/plugins/<id>` and import nothing but the contract; only
`internal/plugins/builtin` lists them, and only the `app` package assembles the registry the
rest of the core is given. Tests enforce both rules. The front-end has the same rule, checked
by a test: a single module file may import feature code.

## 6.2 Pipeline capabilities

A pipeline capability is declared by its plugin: the Tekton Pipeline and Tasks it ships, or the
ones it expects the operator to provide; its parameters; the artifacts it publishes and the
parser each file is read with; the privileges it needs; and how its runs are **read**: by an
interpreter written in Go, a reading declared in YAML (§6.3) or an interpreter compiled to
WebAssembly (§6.4), one way and only one. Capabilities are named
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

The built-in plugins are `core` (build, re-tag), `php` (static analysis, unit tests), `mse`
(integration and end-to-end tests), `security` (the merged security audit), `k6` (load
campaigns, observed rather than launched), `redmine` (tracker), `gitea` (forge) and
`bon-de-livraison` (the delivery note).

## 6.3 Declared readings

A capability whose runs produce test cases can declare its reading instead of coding it:
`capability.Pipeline.Reading` carries YAML the dashboard executes, checked when the plugin is
loaded. `php/static-tests`, `php/unit-tests` and `mse/integration-tests` are read this way
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
<case>...` (or a `.wasm`, §6.4) reads recorded cases without a database nor a cluster. A case is a directory with
`state.json` (the run's state), `records.json` (`[{"tool", "parser", "file"}]`, a tool without
a file having left no report) and `expected.json` (the facts); `--write` records the facts
produced where `expected.json` is missing. The built-in readings' cases live in
`app/testdata/readings/`.

## 6.4 WebAssembly interpreters

What a declaration cannot say, or a format the dashboard does not parse, is read by an
interpreter compiled to WebAssembly: `capability.Pipeline.Module` carries the module, the
dashboard runs it in a sandbox. It is the one way a plugin parses a format itself: the artifact is
declared with the `raw` parser, and the module receives its bytes.

**The sandbox.** The module gets an empty WASI: no directory, no environment variable, no
argument, no network, no host function of the dashboard's. A module importing anything else is
refused when the plugin is loaded. Every run gets a fresh instance, so nothing passes from one run
to the next.

| Limit | Value | Beyond |
|---|---|---|
| module size | 32 MiB | refused at load |
| memory | 64 MiB | the reading fails |
| duration of a reading | 10 s | the instance is closed, the reading fails |
| answer | 16 MiB | the reading fails |
| console (stdout and stderr) | 64 KiB | the rest is dropped; what is kept is logged |

A reading that fails leaves the run read with no facts, as any interpreter's error does; the
dashboard checks the facts a module returns like any other's, and refuses an attachment the run
did not keep.

**Writing one in Go.** `sdk/guest` does the exchange; the interpreter registers in an `init`,
since the module runs as a reactor whose `main` is never called:

```go
package main

import "forge.cadoles.com/vcarroy/arkhitekton/sdk/guest"

func init() { guest.Register(reader{}) } // reader implements capability.Interpreter

func main() {}
```

```sh
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o reader.wasm .
```

`sdk/examples/gotest` is a complete example: it reads the event stream of `go test -json`, a test
per case, a package that does not build as a case of its own, a stream cut off as tests not run.

**The exchange** (ABI 1), for another language: three exports, `arkhitekton_abi() i32` returning
1, `arkhitekton_alloc(size i32) i32` reserving the input, `arkhitekton_interpret(ptr, len i32) i64`
returning the answer's `ptr<<32 | len`. The input is the JSON of `guest.Input` (the run's state and
the records, the payload of a parsed report as JSON, the bytes of a `raw` one in base64), the
answer that of `guest.Output` (facts, or an error). The sdk's types travel by their Go field
names, versioned with `sdk.APIVersion`; a k6 metric's error does not travel.

**Checking it.** `arkhitekton check-reading` takes a `.wasm` like a declaration, and runs it
through the same sandbox on recorded cases (`app/testdata/readings/gotest/`).

## 6.5 Trackers, forges, probes and actions

A plugin that brings a tracker or a forge returns a client for a **target**: the project's
repository and the plugin's settings for that project, resolved and unsealed by the dashboard,
with a timeout the deployment sets. The plugin never sees the database, and never chooses whom
it talks to: the target comes from what an administrator typed. The deployment's `TRACKER_*`
and `FORGE_*` variables only seed those settings.

At most one enabled plugin brings the instance's tracker, and one its forge: the
administration screen refuses a second. A project with none falls back to what works without
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
suffix, which the core prefixes with the plugin id — the MSE plugin's `release.promoted`
becomes the event type `mse.release.promoted`. Prefixing is not decoration: two plugins may both have
something to say about "deployed".

An event declares the shape of its payload; an action declares the fields it takes, any
of which may be a template filled from the event that triggered it. Together they are what
makes a plugin reachable from the automation screen without the core knowing what it does.

## 6.9 Modules

Some plugins need loops that write the dashboard's tables and talk to the cluster, which a
contract plugin cannot do. Those are **modules**, brought by an extension compiled into a
distribution (§6.13), written against the `host` package, and refused at start-up if their
plugin does not declare the `module` capability.

The worker's supervisor runs a module while its plugin is switched on for at least one
project, and restarts it with its new settings when they change. Switching a module on or
off, or editing its settings, applies within thirty seconds, without a restart. A module that
fails is started again at the next tick; the worker keeps running.

A plugin that needs tables owns them. An extension carries its own migrations and its own
version table (MSE's is `goose_mse_db_version`), applied by the same `migrate` role after the
core's, and the dashboard adds its readiness check (`schema-mse`). The core's schema does not mention the plugin's tables,
and dropping the plugin means dropping its migrations — not editing the core's history.

## 6.10 The MSE plugin

The reference implementation of an extension (§6.13). Besides its pipeline capabilities, it
adds:

- **environments**, declared per project in the catalogue, with their rank, namespace,
  visibility, observability and sensitivity;
- **promotion**: re-tagging an image so that a GitOps controller picks it up, subject to
  the deploy permission of the target environment;
- **observation**: what each environment actually runs, read from Flux (what was applied)
  and from the application itself (what is running, and whether its migrations
  succeeded) — an intention and a fact, kept distinct;
- **sign-offs** per environment on a release.

It is enabled per project. An instance where no project enables it behaves as if it did
not exist: the worker opens neither Flux nor the portal's CRD.

## 6.11 Writing a plugin

A plugin is a Go package importing the `sdk` module and nothing else of the dashboard. Three
ways to bring it:

- **built in**: added to `internal/plugins/builtin`, released with the dashboard;
- **installed** from a registry without recompiling, for a plugin that brings pipelines only
  (§6.12);
- **compiled into a distribution** with what pipelines alone cannot do, routes, tables, loops,
  screens: an extension (§6.13).

 Its version changes
whenever what its interpreters read changes: a replay refuses to read a run with another
version than the one that read it, unless asked.

The licensing is settled: an additional permission under section 7 of the AGPL lets a plugin
that uses only the plugin interface be published under any licence its author chooses, and the
combined work be distributed or served on those terms. See `LICENSE.plugin-exception`.

## 6.12 Installed plugins

An administrator installs a plugin **without recompiling**: a signed OCI artifact, pinned by
digest, pulled and verified by the worker, put in force without a restart. An installed plugin
brings **pipeline capabilities only**: Tekton definitions, what they publish and need, settings,
and a declared reading (§6.3) or a WebAssembly module (§6.4). A tracker, a forge, a probe, an
action or a module is code the dashboard runs with its own privileges, and stays with the plugins
compiled into it.

**The directory.** `arkhitekton-plugin.yaml` at its root, the files it names next to it.
`sdk/examples/gotest` is a complete one:

```yaml
apiVersion: arkhitekton.plugin/v1
id: gotest
version: 1.0.0                # letters, digits, dots and dashes
sdk: "1"                      # the sdk.APIVersion it was built against
label: Go
summary: Tests d'un projet Go, lus depuis le flux de `go test -json`.
settings: { global: [], project: [] }        # capability.Field
pipelines:
  - name: unit-tests
    label: Tests Go
    pipelineRef: go-unit-tests
    tekton: [tekton/pipeline-go-unit-tests.yaml, tekton/task-go-test.yaml]
    externalTasks: [git-clone]
    params:
      repo-url: { source: binding, defaultKey: project.repoUrl }
      revision: { source: input, required: true, label: Révision }
    workspaces:
      - { name: git-credentials, need: source-read }
      - { name: shared-data, volume: { storage: 1Gi } }
    needs: [{ name: source-read, reason: clone the project }]
    artifacts:
      - { name: results, param: results-upload-url, object: go-test.json, format: file,
          members: [{ match: go-test.json, parser: raw, tool: gotest }] }
    produces: [results]
    verification: true
    group: unit
    module: gotest.wasm        # or reading: readings/unit-tests.yaml
```

**What is refused**, when the plugin is published and again when it is installed:

| Refused | Why |
|---|---|
| a key the schema does not have, a service account first | a privilege is the operator's to grant, in the catalogue file |
| a need other than `source-read` and `registry-pull`, a product other than results | pushing images and deploying act on the instance's behalf |
| a Tekton definition other than a `Pipeline` or a `Task`, or two in one file | the dashboard installs Pipelines and Tasks, and installs what it checked |
| `serviceAccountName`, `hostPath`, `hostNetwork`, `hostPID`, `hostIPC`, `hostUsers`, `privileged: true` anywhere in a definition | a run reaches nothing beyond its pod |
| a `resolver` or a `bundle` | a definition fetches nothing its signature does not cover |
| a Task the Pipeline runs that is neither shipped nor declared in `externalTasks` | |
| a path leaving the directory, a link, more than 64 MiB in all | |
| the id of a built-in plugin, a Tekton definition another plugin ships | two plugins would overwrite each other |

**Publishing.** Build what the plugin reads its runs with, then pack, push and sign:

```sh
make example-plugin     # builds sdk/examples/gotest/gotest.wasm
arkhitekton publish-plugin --dir sdk/examples/gotest \
  --ref registry.example/plugins/gotest:1.0.0 --key plugin-key.pem
# published  registry.example/plugins/gotest@sha256:…
# signed by  sha256:…
```

The key is an unencrypted PEM private key, ECDSA or Ed25519; registry credentials are read
from the Docker `config.json`, `--plain-http` reaches a registry without TLS. The signature is
cosign's "simple signing" format (`sha256-<digest>.sig` next to the artifact), so
`cosign sign --key … --tlog-upload=false` signs an artifact the same way. A key pair without
cosign:

```sh
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out plugin-key.pem
openssl pkey -in plugin-key.pem -pubout -out trusted-keys.pem
```

**Installing.** Administration, Plugins: the reference **by digest**, the one
`publish-plugin` printed. A tag can be moved after an administrator looked at what it named; a
digest cannot. The request becomes a `plugin-install` job: the worker pulls the artifact,
checks its signature against `PLUGIN_TRUSTED_KEYS` (§4.14) before fetching the layer, checks
the plugin as above, checks that the capabilities still build with it, stores the layer under
`plugins/<id>/<version>/`, and records the key that signed it. A failure is read on the
installation, and on the job.

**Binding.** An installed capability is launched once bound, from Administration, Capacités:
secrets for its needs, configuration keys for its parameters, volume sizes, a timeout, a
ceiling. Never a service account. The catalogue file's binding, when it has one, wins and is
shown read-only (§5.2).

**Versions.** The first version installed is active. A later one is installed beside it and
waits to be activated: an update is a decision, not a side effect of pulling. Activation is
refused if the capabilities would not build with it, a binding that names a parameter the new
version dropped for one. New runs start with the active version; a run is read by the version
that launched it, while that version is installed.

**Uninstalling.** Removing an inactive version removes that version. Removing the active one
uninstalls the plugin: every version, its bindings and its settings. The worker then withdraws
the Tekton definitions it installed for it (those carrying `arkhitekton/plugin=<id>`); a run
already started keeps going on the copy Tekton made. The facts its runs produced stay, and are
shown without the plugin. A plan naming one of its capabilities fails at launch, naming it.

**Taking effect.** Installing, activating, removing and binding move a counter the api and
the worker watch: each rebuilds its capabilities within thirty seconds, at once on the replica
that took the request. Capabilities that do not build leave the previous ones in force, and are
tried again.

## 6.13 Extensions and distributions

An **extension** brings what a plugin's pipelines cannot: routes, tables and their migrations,
loops under the worker's lease, screens. It is compiled into a **distribution** of the
dashboard: a command of its own that calls `app.Main` with its extensions, and an image built
from it. `cmd/arkhitekton` is the default distribution.

```go
package main

import "forge.cadoles.com/vcarroy/arkhitekton/app"

func main() {
    app.Main(app.Options{
        Version: version, Revision: revision,
        Extensions: []host.Extension{mse.Extension()},
        UI:         web.Dist, // the interface built with the extension's screens
    })
}
```

**The host API.** An extension imports `host`, the contract it implements and what the
dashboard lends it, and nothing else of the dashboard: Go forbids importing `internal/`
from another module, and a test enforces the rule for an extension kept in this repository.
`host.Extension` declares, every field optional:

| Field | What it brings |
|---|---|
| `Plugin` | its descriptor (sdk), pipelines included, registered with the built-in plugins |
| `Stacks` | Tekton definitions added to the reference stacks a project declares |
| `Migrations` | its tables: a `MigrationSet` with a version table of its own, applied after the core's |
| `Seed` | settings filled at start-up, projects completed at the first bootstrap, configuration keys |
| `API` | routes, release deployments and promotions, environment suggestions, version scheme checks |
| `Worker` | modules, the fact of a promotion, values added to a project's pipelines |

The dashboard lends a `Store` (projects, releases, the queue, the audit log, plugin
settings), the PostgreSQL pool for the extension's own tables, the catalogue, the
authorisation, the plugins' switches, the cluster's configuration (worker) and a logger. Data
is shared through aliases of the dashboard's types, services through narrow interfaces.
`host/hosttest` gives an extension's tests a throwaway database with the dashboard's schema,
an authenticated request, the plugins' switches, and the dashboard's parsers and readings.

**Screens.** An extension's screens are an interface module (`src/plugins/<id>/index.tsx`,
an `AppModule` as default export: routes, navigation, sections and badges of a release, its
facts' labels). A module imports `@arkhitekton/host` (`apps/web/src/host`), React, the router
and react-query, and its own files; a test enforces it. To build a distribution's interface,
the extension's directory is copied into `apps/web/src/plugins/<id>/` of the arkhitekton
version it builds against, and `npm run build` writes where `ARKHITEKTON_WEB_OUT` says: one
bundle, one React, the core's checks applied to the module.

**Versions.** The host API is young: until arkhitekton 1.0, a minor version may change it, and
says so in its notes. An extension requires the arkhitekton version it is built against, and
the `sdk` version that version requires (tags `vX.Y.Z` and `sdk/vX.Y.Z`).

