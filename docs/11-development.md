# 11. Development

## 11.1 Repository layout

    app/                  the binary as a library, the only place plugins are assembled
    cmd/arkhitekton/      the command: app.Main
    host/hosttest/        what an installed plugin's tests borrow from the dashboard
    rpc/                  the native plugins' contract (proto, pluginv1), extension/ the kit
                          a plugin's program imports, examples/ the example plugin
    internal/
      auth/               roles, permissions, sessions
      catalog/            the pipeline catalogue and its validation
      config/             environment configuration and its validation
      http/               API handlers, middleware, routing
      store/              PostgreSQL access and the core's migrations
      worker/             the background loops
      plugin/             plugin descriptors and the registry
      capability/, facts/, parse/   the contract plugins compile against
      plugins/            the built-in plugins, and builtin/ which assembles them
      results/            the quality score
      tekton/, gitea/, redmine/, registry/, blob/   external systems
      web/                the embedded interface build output
    apps/web/             the React sources; src/host/ is what interface modules import,
                          src/plugins/<id>/ where a plugin's module is dropped to build it
    deploy/               kustomize manifests and pipeline definitions
    docs/                 this documentation

## 11.2 Requirements

Go (the version in `go.mod`), Node for the interface, Docker for the integration tests,
and — for the local cluster — kind, kubectl, kustomize and optionally Skaffold.

## 11.3 Building

    make build         # the binary
    make web           # the interface, into internal/web/dist
    make image         # the container image, tagged by commit and dev

The interface is embedded into the binary: `make build` alone produces a binary serving
whatever `internal/web/dist` last contained.

## 11.4 Testing

    make check            # what CI runs: formatting, vet, Go tests, interface checks
    make test             # Go tests; integration tests skip without TEST_DATABASE_URL
    make test-integration # spins up throwaway PostgreSQL and MinIO, runs everything
    make web-check        # typecheck, lint and tests of the interface

`make test-integration` starts `postgres:17-alpine` and MinIO in Docker, waits for a real
query to succeed rather than for a readiness command that lies, runs the suite with
`-p 1`, and removes the containers whatever the outcome.

**Live tests** run against real instances and are opt-in, never part of `make check`:

    REDMINE_LIVE=1 GITEA_LIVE=1 make test-live

**Golden files** record what the built-in readings produce (`app/testdata/golden`).
A change shows as a diff to review; `go test ./app -update` records the new state.
The readings declared in YAML are also compared with the Go readers they replaced, kept as
oracles in `app/oracles_test.go`, on every report and run state.

**Plugin programs** are built by the tests themselves (`go build`, `CGO_ENABLED=0`): the
launcher's in `internal/native/testdata/plugin/` (handshake, crash, slow call, another
protocol), the hooks' in `app/testdata/hooked/`, the example in `rpc/examples/gotest`. No program
is committed. The contract lives in `rpc/proto`; `make proto` regenerates `rpc/pluginv1`
(committed), fetching `buf` and the generators by version.

**A plugin's interface** is built with `apps/web/plugin.vite.ts` from a tree where its components
are dropped in `src/plugins/<id>/`, beside an `entry.ts` that imports `src/styles/plugin.css` and
calls `registerComponent`. Tailwind reads no file git ignores, even named by `@source`: build that
tree outside an ignored path.

**The core's screens** are resources in `internal/ui/resources` (doc 06 §6.14), their components
registered under `core/<name>` by `apps/web/src/screens/core.tsx`. A screen or a block added there
changes `internal/ui/testdata/core.json`, the core's interface as a viewer with every permission
gets it: run `go test ./internal/ui -update` and read the diff, it is what plugins compose with. A
new widget goes in three places: `sdk/ui` (its schema and validation), `apps/web/src/components/
Widget.tsx` (its rendering) and doc 06 §6.14. A new core source, in `sdk/ui.CoreSources` and
`apps/web/src/lib/data.ts`. A template filter, in `sdk/declare/template.go`,
`apps/web/src/lib/template.ts` and the cases both read, `sdk/declare/testdata/display.json`.

**Adding a view** for declared readings (doc 06 §6.3) means adding it to
`internal/declarative/views.go`: its parser, the fields every row carries, the closed values of
enumerated fields, and a sample in `views_test.go`, which checks that every row carries exactly
the announced fields. A view is part of the contract: renaming a field breaks every declaration
that reads it.

## 11.5 Local cluster

    make deploy        # build, load into kind, render and apply the local overlay
    make logs          # follow the API logs
    make undeploy      # remove it; the namespace takes the volumes with it

Or, to have the cluster follow the working tree:

    make dev           # skaffold dev

Two races are inherent to that loop and are not bugs to chase: the image and the
catalogue ConfigMap roll separately, so a new binary can meet an old catalogue, and a
migration can land before the code that expects it. Both resolve at the next rollout.
Writing the code before the migration that changes the schema avoids the second.

The local overlay expects `deploy/overlays/local/secrets/dashboard.env`, which is
gitignored; copy the `.example` next to it and fill it in.

## 11.6 Interface development

    make web-dev       # Vite dev server, API proxied to :8080

The module boundary is enforced by a test: only `src/modules.ts` may import a feature
module's code, so that an instance with a plugin disabled has no dangling imports.

## 11.7 Conventions

**Comments explain why, not what.** The code is expected to say what it does; a comment
is there when the reason is not reconstructible from the code — a constraint, a race, a
decision that looks arbitrary without its cause.

**Tests are named for the behaviour they protect**, and their failure message says what
was expected of the system, not which assertion tripped.

**Commits follow Conventional Commits**, with a body only for what the diff cannot show.

## 11.8 Continuous integration

`.gitea/workflows/ci.yaml` runs the same `make` targets against service containers, so a
green local `make check` and a green CI mean the same thing.
