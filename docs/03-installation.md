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
- the Tasks every plugin shares (`deploy/base/pipelines`), in the Tekton namespace. The dashboard
  installs each plugin's own definitions when the catalogue binds them.

PostgreSQL and the object store are **not** part of the base: the reference overlay
(`deploy/overlays/local`) adds throwaway ones for development. A real deployment points
`DATABASE_DSN` and `MINIO_ENDPOINT` at managed services.

## 3.3 Deploying

The base is a kustomization; write your own overlay on top of it.

**1. Start from the reference overlay**, then remove what only serves the local kind cluster:

    cp -r deploy/overlays/local deploy/overlays/mysite

- `resources/` (the throwaway PostgreSQL and MinIO) and their entries;
- the `components/pipeline-credentials` entry, whose files `make pipeline-credentials` builds for
  a development cluster;
- the literals `PLUGIN_PLAIN_HTTP_REGISTRIES`, `FORGE_TEKTON_REPO_URL`, `PIPELINE_MINIO_BUCKET`,
  `PIPELINE_GIT_SECRET`, `PIPELINE_REGISTRY_SECRET` and `MINIO_REGION`, unless they suit you;
- the `images:` entry, set again at step 5.

**2. Provide the secrets.** The overlay reads them from an env file that is never
committed:

    cp deploy/overlays/mysite/secrets/dashboard.env.example \
       deploy/overlays/mysite/secrets/dashboard.env

Fill in at least `DATABASE_DSN`, `MINIO_ACCESS_KEY`, `MINIO_SECRET_KEY`, `OIDC_ISSUER_URL`,
`OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `BOOTSTRAP_ADMINS` and `SECRETS_KEY`
(`openssl rand -base64 32`). `SECRETS_KEY` is required as soon as `FORGE_TOKEN` or
`TRACKER_API_KEY` is set: the `migrate` Job seals them. Store it outside the database: a backup
is unreadable without it.

**3. Adjust non-secret configuration** in the overlay's `configMapGenerator`:

- `PUBLIC_URL`, which also gives the OIDC callback (`<PUBLIC_URL>/api/v1/auth/callback`, the
  address to declare on the provider);
- `MINIO_ENDPOINT`, and `MINIO_REGION` (default `us-east-1`) for another server;
- `TEKTON_NAMESPACE`: the base's default is `arkhitekton-pipelines`, which must exist. For
  another namespace, set the variable and patch the namespace of the `tekton.dev` resources and
  of the `arkhitekton-tekton` Role and RoleBinding, as the local overlay does.

Set the Ingress host too: the base declares `dashboard.invalid`, which the local overlay
patches (`patches/ingress-host.yaml`). Every variable is listed in
[04 — Configuration](04-configuration.md).

**4. Bind your pipelines** in `deploy/base/config/catalog.yaml`, which becomes the catalogue
ConfigMap: the plugins bring the pipelines, the catalogue says how this instance runs them. The
format is described in [05 — Pipeline catalogue](05-pipeline-catalogue.md). A catalogue that
does not parse stops the process at start-up, deliberately.

**5. Point the image** at a released version (`images:` in the overlay): every version tag
publishes `ghcr.io/cadoles/arkhitekton:<tag>` for `linux/amd64` and `linux/arm64`, with
`v<major>.<minor>`, `v<major>` and `latest` following final versions. To run your own build,
push it to your registry and point `images:` at the `dev-<commit>` tag it prints:

    make image-push IMAGE_REPO=registry.example.com/team/arkhitekton

**6. Create the pipelines' Secrets** in the Tekton namespace. The build pipelines push with the
registry Secret (`regcred` by default, `PIPELINE_REGISTRY_SECRET`); repositories that are not a
project's own are cloned with the git Secret (`git-credentials` by default,
`PIPELINE_GIT_SECRET`). The dashboard names them and never reads them.

**7. Apply.**

    kubectl apply -k deploy/overlays/mysite

The `migrate` Job runs first and the Deployments wait for the schema it applies. A Job cannot be
changed: on later deployments, delete it first.

    kubectl -n arkhitekton delete job arkhitekton-migrate --wait

## 3.4 First login

`BOOTSTRAP_ADMINS` lists the OIDC subjects or usernames that become administrators the first
time they sign in. Without it nobody could grant the first role, and the `api` refuses to start.

`BOOTSTRAP_USERS` assigns any role rather than only administrator:
`alice:releaser,bob:operator`. Both are applied once, by the `migrate` Job: the database is
authoritative afterwards, and editing them later changes nothing.

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

The worker logs, at start-up, the catalogue it loaded and its candidacy for the lease, then,
once it holds the lease, the pipeline target it will use. Those lines are the fastest way to
tell a misconfiguration from a failure.
