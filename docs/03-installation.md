# 3. Installation

## 3.1 Prerequisites

**Required**

| Component | Notes |
|---|---|
| Kubernetes cluster | The reference cluster runs v1.28; the manifests use no API newer than v1.21. An ingress controller is needed to reach the interface. |
| Tekton Pipelines | Installed in a namespace of its own. Arkhitekton never installs it. |
| PostgreSQL | 12 or later — the schema uses `jsonb_path_exists`. The reference deployment and CI run 17. One database, one user. |
| S3-compatible object store | MinIO in the reference deployment. One bucket, credentials that may read and write it. |
| OIDC provider | Any compliant issuer. Gitea is the reference; the provider must allow a confidential client with an authorization-code flow. |

**Optional** — each absent component degrades one feature, none prevents start-up:

| Component | Without it |
|---|---|
| Issue tracker (Redmine) | Versions and issues are created locally instead of mirrored |
| Forge API (Gitea) | The release form accepts free-text refs instead of suggesting branches and tags |
| Container registry | Image digests and tags are not shown |

## 3.2 What gets deployed

The manifests under `deploy/base` create, in one namespace:

- a `ServiceAccount` and two RBAC bindings — a `Role` for the leader `Lease`, one in the
  Tekton namespace (see [09 §9.4](09-security.md#94-kubernetes-rbac));
- a `Job` that runs `migrate` before the rest starts;
- two `Deployment`s: `api` (replicated) and `worker` (single active);
- a `Service` and an `Ingress`;
- a `ConfigMap` holding the pipeline catalogue, and one holding non-secret configuration;
- the pipeline definitions of `deploy/base/pipelines`, in the Tekton namespace.

PostgreSQL and the object store are **not** part of the base: the reference overlay
(`deploy/overlays/local`) adds throwaway ones for development. A real deployment points
`DATABASE_URL` and `MINIO_ENDPOINT` at managed services.

## 3.3 Deploying

The base is a kustomization; write your own overlay on top of it.

**1. Start from the reference overlay.**

    cp -r deploy/overlays/local deploy/overlays/mysite

**2. Provide the secrets.** The overlay reads them from an env file that is never
committed:

    cp deploy/overlays/mysite/secrets/dashboard.env.example \
       deploy/overlays/mysite/secrets/dashboard.env

Fill in at least `DATABASE_URL`, `MINIO_ACCESS_KEY`, `MINIO_SECRET_KEY`,
`OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET` and `BOOTSTRAP_ADMINS`. Add `SECRETS_KEY` if
plugin settings will hold secret values — generate it with `openssl rand -base64 32` and
store it outside the database, since a database backup is worthless without it and
unreadable if it is lost.

**3. Adjust non-secret configuration** in the overlay's `configMapGenerator`:
`PUBLIC_URL`, `OIDC_REDIRECT_URL` (it must be `<PUBLIC_URL>/api/v1/auth/callback`),
`MINIO_ENDPOINT`, and, in `deploy/base/kustomization.yaml`, `TEKTON_NAMESPACE`, whose default
comes from the reference deployment. Every variable is listed in
[04 — Configuration](04-configuration.md).

**4. Declare your pipelines** in `deploy/base/config/catalog.yaml`, which becomes the
catalogue ConfigMap. The format is described in
[05 — Pipeline catalogue](05-pipeline-catalogue.md). A catalogue that does not parse
stops the process at start-up, deliberately.

**5. Point the image** at your registry (`images:` in the overlay), build and push it:

    make image
    make image-push

**6. Apply.**

    kubectl apply -k deploy/overlays/mysite

The `migrate` Job runs first and the Deployments wait for the schema it applies.

## 3.4 First login

`BOOTSTRAP_ADMINS` lists the OIDC subjects or usernames that become administrators the
first time they sign in. Without it nobody can grant the first role and the interface is
unusable — this is the one variable whose absence is silently fatal to onboarding.

`BOOTSTRAP_USERS` assigns any role rather than only administrator:
`alice:releaser,bob:operator`. Both are read once, at start-up, and never again: the
database is authoritative afterwards, and editing them later changes nothing.

## 3.5 First project

A project is created from the interface (**Administration → Projects**) or through
`POST /api/v1/projects`. It needs a slug, a repository, a forge URL, a registry
namespace, and the Dockerfile and build context its images are built from.

Arkhitekton then creates the project's **deploy key**. The public half is shown in the
project screen; add it to the repository on the forge, and mark it enabled. Pipelines
clone with that key, per project — no shared key exists.

## 3.6 Verifying the installation

    kubectl -n <namespace> get pods
    kubectl -n <namespace> logs deploy/arkhitekton-worker | head -40

The API exposes `/healthz` (the process is alive) and `/readyz` (its dependencies
answer). A ready instance reports `postgres`, `schema` and `minio`; a plugin with its own
schema adds a check of its own. See [10 §10.3](10-operations.md#103-health-and-readiness).

The worker logs, at start-up, the catalogue it loaded, the pipeline target it will use
and its candidacy for the lease. Those four lines are the fastest way to tell a
misconfiguration from a failure.
