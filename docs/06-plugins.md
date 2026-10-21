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

The dashboard refuses a plugin built against another major `sdk.APIVersion`. The built-in
plugins live under `internal/plugins/<id>` and import nothing but the contract; only
`internal/plugins/builtin` lists them, and only `cmd/arkhitekton` assembles the registry the
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
rules are declared. A task that ran and left no readable report did not conclude; one that
never started says nothing. A tool may also read its `duration` from the report as a whole.

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
`cmd/arkhitekton/testdata/readings/`.

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
through the same sandbox on recorded cases (`cmd/arkhitekton/testdata/readings/gotest/`).

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
contract plugin cannot do. Those are **modules**: written against the host
(`internal/module`), assembled by `cmd/arkhitekton` next to the plugin that declares the
`module` capability, and refused at start-up if no plugin declares it.

The worker's supervisor runs a module while its plugin is switched on for at least one
project, and restarts it with its new settings when they change. Switching a module on or
off, or editing its settings, applies within thirty seconds, without a restart. A module that
fails is started again at the next tick; the worker keeps running.

A plugin that needs tables owns them. The MSE plugin carries its own migrations and its
own version table (`goose_mse_db_version`), applied by the same `migrate` role, and
exposes its own readiness check. The core's schema does not mention the plugin's tables,
and dropping the plugin means dropping its migrations — not editing the core's history.

## 6.10 The MSE plugin

Shipped with Arkhitekton, and the reference implementation of a module. Besides its pipeline
capabilities, it adds:

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

A plugin is a Go package importing the `sdk` module and nothing else of the dashboard. Today it
is compiled into the binary: add it to `internal/plugins/builtin`. Its version changes
whenever what its interpreters read changes: a replay refuses to read a run with another
version than the one that read it, unless asked.

The licensing is settled: an additional permission under section 7 of the AGPL lets a plugin
that uses only the plugin interface be published under any licence its author chooses, and the
combined work be distributed or served on those terms. See `LICENSE.plugin-exception`.

What remains: plugins packaged as signed OCI artifacts, a declaration or a module and the Tekton
definitions they read, installed by an administrator.
