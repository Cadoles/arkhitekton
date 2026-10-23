# 12. Licensing

Arkhitekton is published under the **GNU AGPL v3 or later** (`LICENSE`). Two boundaries
are carved out of that reciprocity, so that the project stays open to extensions it does
not control.

## 12.1 Why AGPL rather than GPL

Arkhitekton is almost never *distributed*. It is **served**: an instance runs in a
cluster, and its users work with it over the network without ever receiving a copy. Under
the GPL that use triggers no obligation at all, and modified code can stay private
indefinitely.

Section 13 of the AGPL closes exactly that gap: whoever offers the program to remote
users owes them the source of what they are running, modifications included. For a tool
whose value is to be installed and adapted, that clause is the difference between a
commons and a starting point for private forks.

## 12.2 Plugins, and why an exception was needed

A plugin is not a neighbouring process. It is a Go package **compiled into the binary**:
the registry is filled at compile time, and the core only ever sees descriptors. That
boundary is real for the architecture, and says nothing to copyright law — the link is
static, so it is one program.

Under the bare AGPL a third-party plugin would be covered by the AGPL, and its author
would have no choice of licence. **No architectural decision changes that while the link
is made at compile time.** Only two routes open it: an explicit exception, or
out-of-process plugins (gRPC, WASM, an invoked executable). The first is what
`LICENSE.plugin-exception` grants; the second remains possible later without invalidating
anything.

## 12.3 What the exception does

It grants an additional permission under **section 7** of the AGPL. It defines a *plugin
interface* — the designated Go API, the HTTP API, and the declarative formats the program
reads — and allows a work that uses only that interface to be published under any licence
its author chooses, and the combined work to be conveyed **or made available over a
network** on those terms.

That last clause is not decorative. Without it the exception would be useless here: a
closed plugin could be distributed freely and still be caught by section 13 as soon as an
instance served it — which is the only way the product is ever used.

Three deliberate limits:

- **The core stays AGPL.** Modifying Arkhitekton to accommodate a plugin is modifying
  Arkhitekton; those changes are owed.
- **It does not cover a fork.** Copying core code into a plugin leaves the definition.
- **A recipient may remove it** from their own copy, as section 7 provides.

## 12.4 Third-party plugins

The plugin API is the `sdk` module (`forge.cadoles.com/vcarroy/arkhitekton/sdk`), kept in this
repository but outside `internal/`, with its own `go.mod` and a version, `sdk.APIVersion`, that
every plugin declares and the dashboard checks. It imports only the standard library, and the
plugins shipped with the dashboard import nothing else of it (`internal/capabilities`
`boundary_test.go`). `LICENSE.plugin-exception` names that module.

A plugin is compiled into the binary, which is precisely what the exception makes possible, or
brings its reading as a declaration or a WebAssembly module (doc 06 §6.3, §6.4). A module built
against `sdk/guest` uses only the plugin interface, and is covered by the exception like a
compiled plugin; so is a module written in another language, through the interface the
dashboard runs modules with (item (d) of the exception).

An extension compiled into a distribution (doc 06 §6.13) is written against the host API, the
`host`, `host/hosttest` and `app` packages and the interface module `apps/web/src/host`, which
the exception names as part of the plugin interface: its author chooses its licence as for a
compiled plugin, the distribution combining it with the program on those terms.

A plugin installed from a registry (doc 06 §6.12) is a directory: its manifest and Tekton
definitions are YAML the dashboard reads, linked to nothing (§12.5), and its module, when it
has one, is covered as above. Its author chooses its licence; the dashboard checks who signed
it, not under what terms.

## 12.5 Pipelines and catalogues

A pipeline is YAML read by Tekton, a catalogue is YAML read by Arkhitekton. Neither is
linked to the program: writing one creates no derivative work, and the AGPL imposes
nothing on it.

The real risk was elsewhere. **Nobody writes a pipeline from a blank page** — they start
from `task-git-clone.yaml` or `pipeline-build-image.yaml` and adapt. Shipped under the
AGPL, those files would have carried it into every derived pipeline.

They are therefore under **Apache 2.0** (`deploy/base/pipelines/LICENSE`), the licence of
Tekton itself and explicit about patents. `deploy/base/config/catalog.yaml` follows, as a
catalogue template.

The same holds for plugins. **Nobody writes a plugin from a blank page either**: they start
from `sdk/examples/gotest`, its manifest, its Tekton definitions and its interpreter. Under
the AGPL, a plugin started from it would copy source code of the program, and the exception
covers only a plugin that does not. `sdk/examples/` is therefore under Apache 2.0 too
(`sdk/examples/LICENSE`); the `sdk` packages a plugin compiles against stay under the AGPL
and its exception.

## 12.6 Decisions on record

**Copyright holder: Cadoles.** One line, not a list: accepting outside contributions will
mean choosing a DCO or a CLA, which is an open question.

**`AGPL-3.0-or-later`**, not `-only`: without "or later", adopting a future version would
require every contributor's agreement.

**Apache 2.0 for the shipped pipelines and example plugins**, not CC0: attribution stays due
and the patent grant is explicit, which suits code that gets copied verbatim.

**The plugin interface names every way a plugin talks to the dashboard**: the `sdk` module,
the HTTP API, the declarative formats (plugin manifests and declared readings included) and
the interface WebAssembly modules are run through. A format added later is added to the list
in the exception, not left to interpretation.

**No SPDX headers across the source files.** The root `LICENSE`, this document and the
README section establish the regime; headers everywhere would add noise to every diff
without resolving any ambiguity. The places where two regimes meet are marked:
`deploy/base/pipelines/`, `catalog.yaml` and `sdk/examples/`.

**This document is not legal advice.** The wording of the exception deserves review by a
lawyer before the first outside publication; it is the only part of the repository whose
phrasing, rather than whose behaviour, commits anything.
