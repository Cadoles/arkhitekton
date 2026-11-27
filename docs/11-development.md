# 11. Development

## 11.1 Repository layout

    app/                  the binary as a library, the only place plugins are assembled
    cmd/arkhitekton/      the command: app.Main
    host/hosttest/        what an installed plugin's tests borrow from the dashboard
    sdk/                  the plugin contract (capability, facts, parse, declare, manifest, ui),
                          a module of its own (go.work)
    rpc/                  the native plugins' contract (proto, pluginv1), extension/ the kit
                          a plugin's program imports, examples/ the example plugin; a module too
    internal/
      auth/               roles, permissions, sessions
      catalog/            the pipeline catalogue and its validation
      config/             environment configuration and its validation
      http/               API handlers, middleware, routing
      store/              PostgreSQL access and the core's migrations
      worker/             the background loops
      plugin/             the settings plugins declare, validated and layered per project
      capabilities/       the registry of what the plugins bring, and the boundary test
      plugins/            the built-in plugins, and builtin/ which assembles them
      native/             the launcher of installed plugins' programs
      installed/          pulling, checking and recording installed plugins
      ui/, declarative/   the core's screens as resources, the views of declared readings
      results/            the quality score
      tekton/, registry/, blob/   external systems (the tracker and the forge are plugins)
      testdb/             a migrated database per integration test
      web/                the embedded interface build output
    apps/web/             the React sources; src/host/ is what interface modules import,
                          src/plugins/<id>/ where a plugin's module is dropped to build it
    deploy/               kustomize manifests and pipeline definitions
    docs/                 this documentation

## 11.2 Requirements

Go (the line in `go.mod`, 1.26), Node 22, Docker and curl for `make test-integration` and
`make image`. For the local cluster: kind, kubectl, kustomize, kube-linter (`make manifests`,
which `make deploy` runs), Skaffold (`make dev`, `make run-once`), ssh-keygen and ssh-keyscan
(`make pipeline-credentials`). Run `make web-install` once before `make web`, `make web-check`
or `make check`. `make help` lists every target.

## 11.3 Building

    make build         # the binary
    make web           # the interface, into internal/web/dist
    make image         # the container image, tagged dev-<short commit> and dev
    make binaries      # the release binaries and their SHA256SUMS, into dist/

The interface is embedded into the binary: `make build` alone produces a binary serving
whatever `internal/web/dist` last contained.

## 11.4 Testing

    make check            # formatting, vet, Go unit tests, interface checks
    make test             # Go tests; integration tests skip without TEST_DATABASE_URL
    make test-integration # spins up throwaway PostgreSQL and MinIO, runs everything
    make web-check        # typecheck, lint and tests of the interface

`make test-integration` starts `postgres:17-alpine` and MinIO in Docker, waits for a real
query to succeed rather than for a readiness command that lies, runs the suite with
`-p 1`, and removes the containers when the tests end; a run interrupted earlier leaves them
for the next run to remove. The PostgreSQL server runs without
durability or autovacuum, as in CI: it is thrown away, and both only slow the tests down.

The store's and the configuration's tests each get a database of their own, copied
(`CREATE DATABASE … TEMPLATE`) from one migrated once per test binary (`internal/testdb`): the
account behind `TEST_DATABASE_URL` must be allowed to create databases. The databases a binary
leaves behind are dropped by the next one.

**Live tests** run against real instances and are opt-in, never part of `make check`. Their
values come from your environment, for instance the local `dashboard.env`:

    set -a; . deploy/overlays/local/secrets/dashboard.env; set +a
    REDMINE_LIVE=1 REDMINE_URL=… REDMINE_API_KEY=… REDMINE_PROJECT=… make test-live

Gitea's take `GITEA_LIVE=1`, `GITEA_URL`, `GITEA_TOKEN`, `GITEA_REPO` and `GITEA_COMMIT`. With
`TEST_DATABASE_URL` set, the worker's live loop drops that database's schema: give it a
throwaway one.

**Golden files** record what the built-in readings produce (`app/testdata/golden`).
A change shows as a diff to review; `go test ./app -update` records the new state.
The readings declared in YAML are also compared with the Go readers they replaced, kept as
oracles in `app/oracles_test.go`, for every report and run state in `app/testdata`.

**Plugin programs** are built by the tests themselves (`go build`, `CGO_ENABLED=0`), and none is
committed:

- the launcher's, in `internal/native/testdata/plugin/`: handshake, crash, slow call, another
  protocol;
- the hooks', in `app/testdata/hooked/`;
- the example, in `rpc/examples/gotest`;
- `app/testdata/providers/`, which serves the core's Redmine and Gitea code. `app/providers_test.go`
  reads the same fake instances through it and through the compiled plugins, and wants the same
  answers. `make providers-demo` builds it for publishing, to try an installed tracker, forge and
  purge on a cluster.

The contract lives in `rpc/proto`; `make proto` regenerates `rpc/pluginv1` (committed), fetching
`buf` and the generators by version.

**A plugin's interface** is built with `apps/web/plugin.vite.ts` from a tree where its components
are dropped in `src/plugins/<id>/`, beside an `entry.ts` that imports `@arkhitekton/host/plugin.css`
and calls `registerComponent`:

    ARKHITEKTON_PLUGIN_ENTRY=src/plugins/<id>/entry.ts ARKHITEKTON_PLUGIN_OUT=<dir> \
      npx vite build --config plugin.vite.ts

Tailwind skips gitignored files even when `@source` names them: build that tree outside an
ignored path.

**The core's screens** are resources in `internal/ui/resources` (doc 06 §6.14), their components
registered under `core/<name>` by `apps/web/src/screens/core.tsx`. A screen or a block added there
changes `internal/ui/testdata/core.json`, the core's interface as seen by a viewer with every
permission: run `go test ./internal/ui -update` and read the diff, it is what plugins compose with. A
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

It needs a kind cluster named `arkhitekton` (`KIND_CLUSTER`) with Tekton installed: the overlay
runs the pipelines in `arkhitekton-pipelines`, serves the dashboard at
`dashboard.arkhitekton.local`, and trusts the plain-HTTP registry `kind-registry:5000` for
plugins. An overlay of your own can build on it to change any of these, as a deployment's
repository does for its environment. Copy
`deploy/overlays/local/secrets/dashboard.env.example` to `dashboard.env` and fill it in, then
build the pipelines' credentials once:

    make pipeline-credentials   # a key pair, the forge's host key, registry credentials

To have the cluster follow the working tree, which is preferred:

    make dev           # Skaffold loop, the API forwarded to localhost:8080
    make run-once      # one Skaffold deployment

Without Skaffold:

    make deploy        # build, load into kind, render and apply the local overlay, restart
    make manifests     # render and lint the overlay only
    make logs          # follow the API logs
    make undeploy      # remove it; the namespace takes the volumes with it

Two races are inherent to that loop and are not bugs to chase: the image and the
catalogue ConfigMap roll separately, so a new binary can meet an old catalogue, and a
migration can land before the code that expects it. Both resolve at the next rollout.
Writing the code before the migration that changes the schema avoids the second.

## 11.6 Interface development

    make web-dev       # Vite dev server, API proxied to 127.0.0.1:8080

`make dev` forwards the API there; `VITE_API_TARGET` names another backend.

The module boundary is enforced by `src/modules.test.ts`: no core file imports from
`src/plugins/`, and a plugin module imports only its own files, `@arkhitekton/host` (and its
`plugin.css`), `react`, `react-router` and `@tanstack/react-query`. An instance with a plugin
disabled therefore has no dangling imports.

## 11.7 Conventions

**Comments explain why, not what.** The code is expected to say what it does; a comment
is there when the reason is not reconstructible from the code: a constraint, a race, a
decision that looks arbitrary without its cause.

**Tests are named for the behaviour they protect**, and their failure message says what
was expected of the system, not which assertion tripped.

**Commits follow Conventional Commits**, with a body only for what the diff cannot show.

## 11.8 Continuous integration

`.github/workflows/ci.yaml` runs on pushes to `main`, on `v*` tags and on pull requests. Its jobs
run the same `make` targets as a workstation: `lint` and `test-integration` (which starts
PostgreSQL and MinIO), `vuln`, `web-check` and `web`, `manifests`, and an image build outside
tags. A green CI is therefore `make check` plus
`make test-integration`, `make vuln`, `make web`, `make manifests` and `make image`. Go is the latest patch of the line
the image builds with, not go.mod's minimum: govulncheck judges the toolchain that runs it.

A `v*` tag releases, once every check has passed:

| What | Where |
|---|---|
| The image, `linux/amd64` and `linux/arm64` | `ghcr.io/cadoles/arkhitekton:<tag>`, plus `v<major>.<minor>`, `v<major>` and `latest` for a final version |
| The binaries and `SHA256SUMS` (`make binaries`) | Attached to the GitHub release for the tag |
| The notes | The release's body: the image's digest and the commits since the previous version tag |

The image goes to the GitHub Container Registry and the release to the repository, both with the
job's own token (`GITHUB_TOKEN`): no secret is to be declared. A tag with a suffix (`v1.2.0-rc.1`)
is a pre-release and moves no tag of the image. Replaying the job updates the release and replaces
its files.
