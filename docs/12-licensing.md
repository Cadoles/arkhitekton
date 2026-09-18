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

## 12.4 What is still missing for third-party plugins

A third-party plugin is **not yet technically possible**: the plugin API lives under
`internal/`, which Go forbids importing from another module. In order:

1. Move the designated API out of `internal/`, keeping the test that stops the core from
   importing a plugin by name.
2. State what is stable in it — descriptors, event types, actions — since a published
   interface is a contract.
3. Choose between compile-time loading, where the plugin author ships a rebuilt binary
   (which is precisely what the exception makes possible), and out-of-process loading,
   where the licensing question fades and the API surface grows.

When the API moves, `LICENSE.plugin-exception` must follow: it names the designated
package "at the date of this file".

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

## 12.6 Decisions on record

**Copyright holder: Cadoles.** One line, not a list: accepting outside contributions will
mean choosing a DCO or a CLA, which is an open question.

**`AGPL-3.0-or-later`**, not `-only`: without "or later", adopting a future version would
require every contributor's agreement.

**Apache 2.0 for the shipped pipelines**, not CC0: attribution stays due and the patent
grant is explicit, which suits infrastructure code that gets copied verbatim.

**No SPDX headers across the source files.** The root `LICENSE`, this document and the
README section establish the regime; headers everywhere would add noise to every diff
without resolving any ambiguity. The two places where two regimes meet —
`deploy/base/pipelines/` and `catalog.yaml` — are marked.

**This document is not legal advice.** The wording of the exception deserves review by a
lawyer before the first outside publication; it is the only part of the repository whose
phrasing, rather than whose behaviour, commits anything.
