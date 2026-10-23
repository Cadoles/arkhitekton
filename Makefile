BINARY := bin/arkhitekton
GO     ?= go

# The Go targets name our packages explicitly, and not `./...`.
#
# `apps/web/node_modules` contains at least one Go file — an npm dependency that ships a Go
# port of its algorithm. `./...` compiles it, `go vet` analyses it and `gofmt -l` would report
# it: continuous integration would fail on a third party's code, for a reason impossible to
# guess from the message.
GO_PKGS := ./app/... ./cmd/... ./host/... ./internal/... ./sdk/...
GO_DIRS := app cmd host internal sdk

# A personal registry during development: the dashboard's images do not go into `cnous-mse`
# until the project is deliverable.
IMAGE_REPO ?= reg.cadoles.com/vcarroy/arkhitekton
# The tag carries the commit: republishing under the same tag would not be enough to make the
# cluster take the image again, the pods setting no `imagePullPolicy`.
IMAGE_TAG  ?= dev-$(shell git rev-parse --short HEAD 2>/dev/null || echo inconnue)
IMAGE      := $(IMAGE_REPO):$(IMAGE_TAG)

# The tag the local overlay consumes. Distinct from IMAGE_TAG: locally the image is injected
# straight into the kind nodes, without going through a registry.
LOCAL_IMAGE := $(IMAGE_REPO):dev
KIND_CLUSTER ?= mse
OVERLAY ?= deploy/overlays/local

# The namespace where the Secrets the pipelines mount live — among them the per-project deploy
# keys. Distinct from the dashboard's.
TEKTON_NAMESPACE ?= mse-tekton-pipelines

PG_TEST_NAME := pg-arkhitekton-test
PG_TEST_PORT := 15432
PG_TEST_DSN  := postgres://postgres:t3st@127.0.0.1:$(PG_TEST_PORT)/mse?sslmode=disable

MINIO_TEST_NAME   := minio-arkhitekton-test
MINIO_TEST_PORT   := 19000
MINIO_TEST_BUCKET := arkhitekton-test

.PHONY: help build example-plugin test test-integration test-ci lint fmt fmt-check vet vuln clean check dev run-once \
        web web-install web-dev web-check dev-prereqs image image-push kind-load manifests deploy undeploy logs

help: ## Show this help
	@grep -hE '^[a-z-]+:.*##' $(MAKEFILE_LIST) | sed -E 's/:.*## /\t/' | expand -t24

build: ## Build the binary
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags='-s -w' -o $(BINARY) ./cmd/arkhitekton

example-plugin: ## Build the example plugin's module, ready for publish-plugin
	cd sdk && GOOS=wasip1 GOARCH=wasm $(GO) build -buildmode=c-shared -o examples/gotest/gotest.wasm ./examples/gotest

test: ## Run the tests (the integration tests are skipped without TEST_DATABASE_URL)
	$(GO) test $(GO_PKGS)

# The credentials come from **your** environment and are not read here: they are those of a
# production instance, and a target that fetched them on its own would make this a command one
# runs absent-mindedly.
#
# In development they live in `deploy/overlays/local/secrets/dashboard.env` — outside the
# repository — under their configuration names:
#
#   set -a; . deploy/overlays/local/secrets/dashboard.env; set +a
#   REDMINE_LIVE=1 REDMINE_URL=$$TRACKER_URL REDMINE_PROJECT=$$TRACKER_PROJECT \
#     REDMINE_API_KEY=$$TRACKER_API_KEY make test-live
#
# -count=1 disables the cache: a "cached" test queried nobody.
test-live: ## Checks against the real instances (REDMINE_LIVE=1, GITEA_LIVE=1)
	@if [ "$$REDMINE_LIVE" != "1" ] && [ "$$GITEA_LIVE" != "1" ]; then \
		echo "no real check requested."; \
		echo "  REDMINE_LIVE=1 REDMINE_URL=… REDMINE_API_KEY=… REDMINE_PROJECT=… make test-live"; \
		exit 1; \
	fi
	@if [ -n "$$TEST_DATABASE_URL" ]; then \
		echo "WARNING: the whole loop runs 'drop schema public cascade' on"; \
		echo "TEST_DATABASE_URL. That database must be throwaway."; \
	fi
	$(GO) test -count=1 -run Live -v $(GO_PKGS)

# -p 1 serialises the packages: `store`'s tests reset the schema, and nothing would guarantee
# the order with those that depend on it.
# -count=1 disables the cache: a "cached" integration test checked nothing.
test-integration: ## Tests with a throwaway PostgreSQL and MinIO
	@docker rm -f $(PG_TEST_NAME) $(MINIO_TEST_NAME) >/dev/null 2>&1 || true
	@docker run -d --name $(PG_TEST_NAME) -e POSTGRES_PASSWORD=t3st -e POSTGRES_DB=mse \
		-p $(PG_TEST_PORT):5432 postgres:17-alpine >/dev/null
	@docker run -d --name $(MINIO_TEST_NAME) -p $(MINIO_TEST_PORT):9000 \
		-e MINIO_ROOT_USER=minioadmin -e MINIO_ROOT_PASSWORD=minioadmin \
		cgr.dev/chainguard/minio:latest server /data >/dev/null
	@# The bucket is created by the test harness itself: doing it here would force
	@# every runtime environment to redo it in its own way.
	@until curl -sf http://127.0.0.1:$(MINIO_TEST_PORT)/minio/health/live >/dev/null 2>&1; do sleep 0.5; done
	@# pg_isready answers "ready" on initdb's temporary server, before the final
	@# server accepts TCP connections. Waiting for a real query on the target
	@# database is the only reliable signal.
	@until docker exec $(PG_TEST_NAME) psql -U postgres -d mse -c 'select 1' >/dev/null 2>&1; do sleep 0.5; done
	@TEST_DATABASE_URL=$(PG_TEST_DSN) \
		TEST_MINIO_ENDPOINT=127.0.0.1:$(MINIO_TEST_PORT) \
		TEST_MINIO_BUCKET=$(MINIO_TEST_BUCKET) \
		$(GO) test -p 1 -count=1 $(GO_PKGS) ; status=$$? ; \
		docker rm -f $(PG_TEST_NAME) $(MINIO_TEST_NAME) >/dev/null 2>&1 ; exit $$status

# A variant for an environment that already supplies PostgreSQL and MinIO — continuous
# integration services, or containers started by hand. The flags are thus defined in a single
# place: CI and the workstation run the same thing.
test-ci: ## Integration tests against services already started
	$(GO) test -p 1 -count=1 $(GO_PKGS)

fmt: ## Reformate le code
	$(GO) fmt $(GO_PKGS)

# `go fmt` rewrites the files, so it always succeeds: using it in continuous integration would
# let badly formatted code through after fixing it in a working tree thrown away at the end of
# the job. The check must observe, not correct.
fmt-check: ## Fails if a file is not formatted
	@unformatted=$$(gofmt -l $(GO_DIRS)) ; \
	if [ -n "$$unformatted" ] ; then \
		echo "unformatted files (run 'make fmt'):" ; \
		echo "$$unformatted" ; \
		exit 1 ; \
	fi

vet: ## The toolchain's static analysis
	$(GO) vet $(GO_PKGS)
	@# The guest side of WebAssembly interpreters only builds for wasip1.
	GOOS=wasip1 GOARCH=wasm $(GO) vet ./sdk/guest/

lint: fmt-check vet ## Formatting check + static analysis

vuln: ## Looks for vulnerabilities in the dependencies
	# Goes through a script rather than calling govulncheck directly: govulncheck offers no
	# exclusion mechanism, and a vulnerability with no upstream fix would make the job red forever.
	# The exceptions are declared and justified in the script, displayed again at every run, and an
	# exception that has become useless makes the check fail.
	./scripts/vulncheck.sh $(GO_PKGS)

# A single target reproducing what CI checks: a developer must be able to observe locally the
# failure they would get in integration. The promise holds only if none of the targets called
# modifies the working tree.
check: lint test web-check ## What CI checks

clean: ## Supprime les artefacts de build
	rm -rf bin/

# --- Interface ----------------------------------------------------------------

WEB := apps/web

web-install: ## Installs the interface's dependencies
	npm --prefix $(WEB) ci --no-audit --no-fund

# Vite writes straight into internal/web/dist, where `//go:embed` reads it: there is no copy
# step, hence no way of forgetting it.
web: ## Construit l'interface dans internal/web/dist
	npm --prefix $(WEB) run build

web-dev: ## Serves the interface in development, API proxied to :8080
	npm --prefix $(WEB) run dev

web-check: ## Typage, analyse statique et tests de l'interface
	npm --prefix $(WEB) run typecheck
	npm --prefix $(WEB) run lint
	npm --prefix $(WEB) run test

# --- Development environment --------------------------------------------------

# Flux CRDs and development objects, with no controller at all. The PortalData CRD belongs to
# the `mse` repository and is not copied here: the target fails saying how to install it rather
# than shipping a copy that would diverge.
dev-prereqs: ## Installs the Flux CRDs and the development Flux objects
	./hack/dev-prereqs/install-crds.sh

# --- Image and deployment -----------------------------------------------------

# The Git secret is mounted as Skaffold does it, and for the same reason: the
# `tekton-mse-manager` module lives in a private repository, and `go mod download` cannot
# authenticate without it. Its absence here made this target — hence `make deploy`, the only
# deployment path without Skaffold — unusable: it failed on "could not read Username for
# forge.cadoles.com".
#
# A BuildKit secret leaves no trace in the image's layers, unlike a `COPY`, which would engrave
# them there for good.
image: ## Builds the image, tagged by commit and `dev`
	DOCKER_BUILDKIT=1 docker build \
		--secret id=git-credentials,src=$(HOME)/.git-credentials \
		--build-arg VERSION=$(IMAGE_TAG) \
		--build-arg REVISION=$$(git rev-parse HEAD 2>/dev/null || echo inconnue) \
		-t $(IMAGE) -t $(LOCAL_IMAGE) .

image-push: image ## Pousse l'image sur le registre
	docker push $(IMAGE)

# Injected straight into the kind nodes: avoids the round trip through the registry, which
# largely dominates the time of a local iteration.
kind-load: image ## Charge l'image dans le cluster kind
	kind load docker-image $(LOCAL_IMAGE) --name $(KIND_CLUSTER)

manifests: ## Rend et analyse les manifests de l'overlay
	@kustomize build $(OVERLAY) > /dev/null
	@kustomize build $(OVERLAY) | kube-linter lint -

# The Jobs are deleted before applying: a Job's `spec.template` is immutable, and a plain
# `apply` would fail on "field is immutable" as soon as the image tag or a configuration
# variable changes.
# Kept for a one-off deployment without Skaffold. Prefer `make dev`: the `dev` tag being
# mutable, this target must force a restart, and forgetting that restart gives a cluster
# running old code while displaying up-to-date manifests.
# A one-off repair of the deploy keys written before the fix.
#
# They carried only `ssh-privatekey`, the name the `kubernetes.io/ssh-auth` type imposes and
# which SSH does not read: the `ssh-directory` workspace is copied into `~/.ssh`, where only
# `id_ed25519` is loaded. Every build failed with "Permission denied (publickey)".
#
# Why on the operator's side and not in the dashboard: the dashboard has no `get` on Secrets
# (docs/09-security.md), so it cannot read a key back in order to copy it under a second name.
# Its only possible recovery would be a **rotation**, which would mean pasting as many public
# keys as there are projects. `kubectl`, for its part, reads — and the key does not change.
#
# Without effect on a secret already repaired, and without effect on the projects created after
# the fix.
deploy-keys-repair: ## Adds id_ed25519 to the deploy keys written before the fix
	@for secret in $$(kubectl -n $(TEKTON_NAMESPACE) get secrets \
	    -l app.kubernetes.io/managed-by=arkhitekton -o name); do \
	  if kubectl -n $(TEKTON_NAMESPACE) get $$secret \
	      -o jsonpath='{.data.id_ed25519}' | grep -q .; then \
	    echo "$$secret: already repaired"; \
	    continue; \
	  fi; \
	  key=$$(kubectl -n $(TEKTON_NAMESPACE) get $$secret -o jsonpath='{.data.ssh-privatekey}'); \
	  if [ -z "$$key" ]; then echo "$$secret: no private key, skipped"; continue; fi; \
	  kubectl -n $(TEKTON_NAMESPACE) patch $$secret --type merge \
	    -p "{\"data\":{\"id_ed25519\":\"$$key\"}}" >/dev/null && echo "$$secret: repaired"; \
	done

deploy: kind-load manifests ## Deploys on kind (local overlay, without Skaffold)
	@# A Job is immutable: the `apply` that follows fails on "field is immutable"
	@# as long as the previous one has not disappeared. `--wait` alone was not
	@# enough — the target failed then succeeded with no change other than the
	@# re-run — hence the explicit wait for the disappearance, which is not
	@# satisfied by the deletion request alone.
	kubectl delete job -n arkhitekton arkhitekton-migrate arkhitekton-minio-bucket \
		--ignore-not-found --wait=true
	kubectl wait --for=delete -n arkhitekton --timeout=60s \
		job/arkhitekton-migrate job/arkhitekton-minio-bucket 2>/dev/null || true
	kustomize build $(OVERLAY) | kubectl apply -f -
	kubectl wait --for=condition=complete --timeout=180s -n arkhitekton job/arkhitekton-migrate
	@# The `dev` tag does not change from one build to the next: with no forced
	@# restart, the pods would keep the image already present on the node.
	kubectl rollout restart -n arkhitekton deployment/arkhitekton-api deployment/arkhitekton-worker
	kubectl rollout status  -n arkhitekton deployment/arkhitekton-api --timeout=180s
	kubectl rollout status  -n arkhitekton deployment/arkhitekton-worker --timeout=180s

undeploy: ## Deletes the deployment (the namespace takes the PVCs with it)
	kustomize build $(OVERLAY) | kubectl delete --ignore-not-found -f -

logs: ## Suit les journaux de l'api
	kubectl logs -n arkhitekton -l app.kubernetes.io/component=api --tail=100 -f

# --- Development loop ---------------------------------------------------------

# `--cleanup=false` on both targets, and it is no detail of comfort.
#
# Skaffold cleans up by default when it is interrupted **or when it fails**: it deletes what it
# deployed, namespace included, and the namespace takes the PVCs with it. A failure to load an
# image into kind — which happens, `ctr` sometimes refusing an image that is already present —
# therefore destroyed PostgreSQL and MinIO.
#
# What then disappears cannot be recovered: the runs and the versions are rebuilt from Tekton
# and Redmine, but the **releases**, the **jobs** and the **audit log** exist only in this
# database. It has happened.
#
# To start from scratch deliberately: `make undeploy`.
dev: ## Skaffold loop: rebuilds and redeploys at every change
	skaffold dev --cleanup=false

run-once: ## Builds and deploys once through Skaffold
	skaffold run --cleanup=false

SECRETS_DIR := deploy/overlays/local/components/pipeline-credentials

.PHONY: pipeline-credentials
pipeline-credentials: ## Prepares the credentials the pipelines will use (local)
	@# Builds the three files the local overlay mounts as secrets, in the
	@# PipelineRuns' namespace. None of them is versioned.
	@#
	@# They are distinct from `mse-tekton`'s, which target the real application:
	@# there the key is a deploy key of `CNOUS/mse`, and the registry account has
	@# `push` on `cnous-mse` only.
	@set -eu; \
	mkdir -p $(SECRETS_DIR); \
	if [ ! -f $(SECRETS_DIR)/pipeline-ssh-key ]; then \
	  echo '==> generating a dedicated key pair'; \
	  ssh-keygen -t ed25519 -C arkhitekton-pipelines -N '' \
	    -f $(SECRETS_DIR)/pipeline-ssh-key >/dev/null; \
	  mv $(SECRETS_DIR)/pipeline-ssh-key.pub $(SECRETS_DIR)/pipeline-ssh-key.pub.txt; \
	else \
	  echo '==> key already present, kept'; \
	fi; \
	if [ ! -f $(SECRETS_DIR)/pipeline-known-hosts ]; then \
	  echo '==> reading the forge host key'; \
	  ssh-keyscan -p $${FORGE_SSH_PORT:-2222} $${FORGE_HOST:-forge.cadoles.com} \
	    > $(SECRETS_DIR)/pipeline-known-hosts 2>/dev/null; \
	fi; \
	if [ ! -f $(SECRETS_DIR)/pipeline-dockerconfig.json ]; then \
	  echo '==> registry credentials'; \
	  printf 'Server [https://reg.cadoles.com]: '; read -r server; \
	  server=$${server:-https://reg.cadoles.com}; \
	  printf 'Username: '; read -r user; \
	  printf 'Password: '; stty -echo; read -r pass; stty echo; echo; \
	  kubectl create secret docker-registry tmp-regcred \
	    --docker-server="$$server" --docker-username="$$user" --docker-password="$$pass" \
	    --dry-run=client -o jsonpath='{.data.\.dockerconfigjson}' \
	    | base64 -d > $(SECRETS_DIR)/pipeline-dockerconfig.json; \
	  unset pass; \
	fi; \
	chmod 600 $(SECRETS_DIR)/pipeline-ssh-key $(SECRETS_DIR)/pipeline-dockerconfig.json; \
	echo; \
	echo 'Two steps remain, on your own accounts:'; \
	echo; \
	echo '  1. Declare this public key READ-ONLY on the target repository'; \
	echo '     (Gitea -> repository -> Settings -> Deploy keys):'; \
	echo; \
	sed 's/^/     /' $(SECRETS_DIR)/pipeline-ssh-key.pub.txt; \
	echo; \
	echo '  2. Give the registry account write access on the target project'; \
	echo '     (Harbor -> project -> Members -> Developer role).'; \
	echo; \
	echo 'Then enable these secrets in dashboard.env:'; \
	echo '     PIPELINE_GIT_SECRET=arkhitekton-pipeline-git'; \
	echo '     PIPELINE_REGISTRY_SECRET=arkhitekton-pipeline-registry'
