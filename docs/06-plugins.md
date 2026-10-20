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

## 6.3 Trackers, forges, probes and actions

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

## 6.4 Settings

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

## 6.5 Per-project activation

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

## 6.6 Events and actions

A plugin may declare **events** it emits and **actions** it offers. Both are named by a
suffix, which the core prefixes with the plugin id — the MSE plugin's `release.promoted`
becomes the event type `mse.release.promoted`. Prefixing is not decoration: two plugins may both have
something to say about "deployed".

An event declares the shape of its payload; an action declares the fields it takes, any
of which may be a template filled from the event that triggered it. Together they are what
makes a plugin reachable from the automation screen without the core knowing what it does.

## 6.7 Modules

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

## 6.8 The MSE plugin

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

## 6.9 Writing a plugin

A plugin is a Go package importing the `sdk` module and nothing else of the dashboard. Today it
is compiled into the binary: add it to `internal/plugins/builtin`. Its version changes
whenever what its interpreters read changes: a replay refuses to read a run with another
version than the one that read it, unless asked.

The licensing is settled: an additional permission under section 7 of the AGPL lets a plugin
that uses only the plugin interface be published under any licence its author chooses, and the
combined work be distributed or served on those terms. See `LICENSE.plugin-exception`.

What remains, in order: interpreters declared in YAML for the common case; interpreters
compiled to WebAssembly for the rest; plugins packaged as signed OCI artifacts, installed by an
administrator.
