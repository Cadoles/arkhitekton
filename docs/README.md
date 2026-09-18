# Arkhitekton — Technical documentation

Arkhitekton is a release dashboard: it sits between an issue tracker, a forge, Tekton and
a container registry, and shows what the state of a version is and what remains to be done
before it ships.

This documentation describes the software as it is. It is reference material, not a plan.

| Document | Contents |
|---|---|
| [01 — Overview](01-overview.md) | What Arkhitekton manages, what it needs, its vocabulary |
| [02 — Architecture](02-architecture.md) | Processes, loops, external systems, plugin boundary |
| [03 — Installation](03-installation.md) | Prerequisites, deployment, bootstrap, first project |
| [04 — Configuration](04-configuration.md) | Every environment variable, its default and its role |
| [05 — Pipeline catalogue](05-pipeline-catalogue.md) | Catalogue format, parameters, uploads, and the contract a pipeline honours |
| [06 — Plugins](06-plugins.md) | Descriptors, settings, per-project activation, the MSE plugin |
| [07 — HTTP API](07-http-api.md) | Every route, its permission, its conventions |
| [08 — Data model](08-data-model.md) | PostgreSQL schema and object store layout |
| [09 — Security](09-security.md) | Authentication, roles, Kubernetes RBAC, secrets |
| [10 — Operations](10-operations.md) | Migrations, leader election, readiness, troubleshooting |
| [11 — Development](11-development.md) | Repository layout, building, testing, local cluster |
| [12 — Licensing](12-licensing.md) | AGPL, the plugin exception, pipelines under Apache 2.0 |

Start with [01 — Overview](01-overview.md) to install and operate an instance, or with
[11 — Development](11-development.md) to work on Arkhitekton itself.
