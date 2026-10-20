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
| `forge` | Where code and refs come from |
| `exporter` | What a release can be exported as |
| `module` | Screens, routes and behaviour, the widest kind |

A plugin's settings, events and actions are declared by a **descriptor**. The core only
ever sees descriptors, never plugin types:

```go
plugin.Descriptor{
    ID:           "mse",
    Capabilities: []capability.Header{{Kind: capability.KindModule, Name: "module", Label: "Module"}},
    Label:        "MSE",
    Summary:    "Environments, GitOps promotion and sign-offs",
    Global:     []plugin.Field{…},   // settings that belong to the instance
    Project:    []plugin.Field{…},   // settings a project may override
    Events:     []plugin.EventSpec{…},
    Actions:    []plugin.ActionSpec{…},
    PerProject: true,                // may be enabled per project
}
```

A Go test enforces that no core package imports a plugin package; only
`internal/plugins/builtin`, which lists the built-in plugins for the binary, does. The front-end has the same rule, checked by a test: a single
module file may import feature code.

## 6.2 Pipeline capabilities

A pipeline capability is declared in Go by its plugin (`sdk/capability`): the Tekton
Pipeline and Tasks it ships, or the ones it expects the operator to provide; its parameters;
the artifacts it publishes and the parser each file is read with; the privileges it needs;
and its **interpreter**. Capabilities are named `plugin/name` (`php/unit-tests`), so two
plugins cannot collide. The [catalogue](05-pipeline-catalogue.md) binds them to an instance.

The interpreter reads a finished run. The dashboard fetches the artifacts the capability
declares, parses each file with a parser of its own (JUnit, mochawesome, PHPStan, PHP-CS-Fixer,
k6, the security report) or hands over the bytes (`raw`) for a format it does not know, and
passes the records with the run's state as Tekton reported it: each task's status and reason,
each step's exit code. The interpreter returns **facts** in a shape the dashboard defines:

| Shape | Identity | Compared by |
|---|---|---|
| `cases` | suite and name | change of status between releases |
| `findings` | fingerprint | appearance and disappearance, severity |
| `measures` | scenario and metric | distance to a pinned baseline |

The set of shapes is closed, and only the core adds one: a shape is a schema, a comparison
and a screen. Each stored result names the plugin version that read it, its shape and its
group, so it stays readable when the plugin changes or goes. A replay reads a run again with
the interpreter, and refuses one another version read unless asked.

The built-in plugins are `core` (build, re-tag), `php` (static analysis, unit tests), `mse`
(integration and end-to-end tests), `security` (the merged security audit) and `k6` (load
campaigns, observed rather than launched).

## 6.3 Settings

A descriptor declares its settings as fields:

```go
plugin.Field{
    Name: "kustomRepoUrl", Label: "GitOps repository",
    Type: plugin.FieldText, Help: "…", Secret: false,
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

## 6.4 Per-project activation

A descriptor with `PerProject: true` can be enabled or disabled project by project. A
plugin without it is instance-wide and does not appear in a project's plugin list.

The state resolves to: enabled here, disabled here, or inherited from the instance.
Asking for a disabled plugin's settings returns a sentinel error rather than empty
values, so that a caller cannot mistake "switched off" for "not configured".

Routes belonging to a module are gated: a request to a module's route, for a project
where it is disabled, is refused by middleware that resolves the project from the path.
The interface hides the screens the same way, from the same source of truth.

## 6.5 Events and actions

A plugin may declare **events** it emits and **actions** it offers. Both are named by a
suffix, which the core prefixes with the plugin id — the MSE plugin's `release.promoted`
becomes the event type `mse.release.promoted`. Prefixing is not decoration: two plugins may both have
something to say about "deployed".

An event declares the shape of its payload; an action declares the fields it takes, any
of which may be a template filled from the event that triggered it. Together they are what
makes a plugin reachable from the automation screen without the core knowing what it does.

## 6.6 A plugin's own schema

A plugin that needs tables owns them. The MSE plugin carries its own migrations and its
own version table (`goose_mse_db_version`), applied by the same `migrate` role, and
exposes its own readiness check. The core's schema does not mention the plugin's tables,
and dropping the plugin means dropping its migrations — not editing the core's history.

## 6.7 The MSE plugin

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
not exist.

## 6.8 Writing a plugin

Third-party plugins are **not possible yet**: the plugin API lives under `internal/`, which
Go forbids importing from another module. Plugins are compiled into the binary and listed by
`internal/plugins/builtin`.

What is settled is the licensing: an additional permission under section 7 of the AGPL
lets a plugin that uses only the plugin interface be published under any licence its
author chooses, and the combined work be distributed or served on those terms. See
`LICENSE.plugin-exception`.

What remains, in order: move the contract (`capability`, `facts`, `parse`) out of
`internal/` and state what is stable in it; interpreters declared in YAML for the common case;
interpreters compiled to WebAssembly for the rest; plugins packaged as signed OCI artifacts,
installed by an administrator.
