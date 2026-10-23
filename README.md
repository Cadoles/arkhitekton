# Arkhitekton

**A release dashboard for teams that ship software from a tracker, a forge, a CI engine
and a registry — and have to look in four places to answer one question.**

Arkhitekton sits between those systems and answers it in one screen: *what is the state of
this version, and what is left to do before it ships?*

It replaces none of them. It reads them, correlates what they say around a release, and
lets a human take the next step — run the missing pipeline, read a failed test, write the
release note, promote an image — without leaving the page.

## What it does

- **Releases end to end.** Create a release from a git ref, watch the plan it owes — build,
  tests, audits — advance on its own, and see what each pipeline found.
- **A queue you can see.** Jobs are queued, dispatched to Tekton, observed until they
  conclude, with their logs streamed and kept.
- **Results, not just exit codes.** Test cases, security findings with their deltas, and
  benchmark measures are parsed from what pipelines upload, and stay addressable per run.
- **Release notes from the tracker.** Issues of a version become a note you can edit and
  export.
- **Several projects per instance.** Each with its own repository, deploy key, registry,
  pipelines and permissions.
- **Roles per project.** A user may be an operator on one project and a viewer on another.
- **Declarative pipelines.** What a pipeline needs and produces is declared in a YAML
  catalogue, not in Arkhitekton's code.
- **Everything domain-specific is a plugin.** Deployment environments, GitOps promotion and
  sign-offs come from a plugin that can be switched off per project.

## How it fits together

One binary, three long-running shapes of it: `api` (replicated, serves the interface and
the API), `worker` (single active instance, runs the loops), `migrate` (applies the
schema, exits). State lives in PostgreSQL; logs and reports live in an S3-compatible
store. See [docs/02 — Architecture](docs/02-architecture.md).

## Requirements

| Required | |
|---|---|
| Kubernetes | With an ingress controller. The reference cluster runs v1.28 |
| Tekton Pipelines | Installed separately; Arkhitekton never installs it |
| PostgreSQL | 12 or later; 17 in the reference deployment |
| S3-compatible object store | MinIO in the reference deployment |
| OIDC provider | Gitea is the reference; any compliant issuer works |

Optional, each degrading one feature rather than blocking start-up: an issue tracker
(Redmine), the forge API (Gitea), a container registry, and Flux for the GitOps plugin.

## Installing

```bash
git clone https://forge.cadoles.com/vcarroy/arkhitekton.git
cd arkhitekton
cp -r deploy/overlays/local deploy/overlays/mysite
cp deploy/overlays/mysite/secrets/dashboard.env.example \
   deploy/overlays/mysite/secrets/dashboard.env
```

Fill in `dashboard.env` — at least the database DSN, the object store keys, the OIDC
client, and `BOOTSTRAP_ADMINS` with the account that will hold the first administrator
role. Adjust `PUBLIC_URL`, `OIDC_REDIRECT_URL` and `TEKTON_NAMESPACE` in the overlay,
declare your pipelines in `deploy/base/config/catalog.yaml`, then:

```bash
make image
make image-push
kubectl apply -k deploy/overlays/mysite
```

The migration Job runs first; the deployments wait for the schema it applies. Sign in,
create a project, and install the deploy key Arkhitekton generates for it on your forge.

Full walkthrough: [docs/03 — Installation](docs/03-installation.md). Every environment
variable: [docs/04 — Configuration](docs/04-configuration.md).

## Developing

```bash
make help              # every available target
make check             # what CI runs
make test-integration  # tests against throwaway PostgreSQL and MinIO
make dev               # Skaffold loop on a local kind cluster
```

See [docs/11 — Development](docs/11-development.md).

## Documentation

[docs/README.md](docs/README.md) is the index: architecture, installation, configuration,
the pipeline catalogue, plugins, the HTTP API, the data model, security, operations.

## Licence

Arkhitekton is published under the **GNU AGPL v3 or later** ([LICENSE](LICENSE)). An
instance served over a network must therefore offer its source, modifications included.

Two boundaries are carved out of that reciprocity:

- **Plugins.** An additional permission under section 7 of the AGPL
  ([LICENSE.plugin-exception](LICENSE.plugin-exception)) lets a plugin that uses only the
  plugin interface be published under any licence its author chooses, and the combined
  work be distributed or served on those terms.
- **Pipelines, catalogues and examples.** Pipelines and catalogues are data read by Tekton
  and Arkhitekton, not linked code: writing one creates no derivative work. The shipped
  examples (`deploy/base/pipelines/`, `deploy/base/config/catalog.yaml`, the example plugins
  under `sdk/examples/`) are under **Apache 2.0**, so they can be copied and adapted freely.

The reasoning is in [docs/12, Licensing](docs/12-licensing.md).

Copyright (C) 2026 Cadoles.
