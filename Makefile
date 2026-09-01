BINARY := bin/mse-dashboard
GO     ?= go

# Les cibles Go visent nos paquets explicitement, et non `./...`.
#
# `apps/web/node_modules` contient au moins un fichier Go — une dépendance npm
# qui embarque un portage Go de son algorithme. `./...` le compile, `go vet`
# l'analyse et `gofmt -l` le signalerait : l'intégration continue échouerait sur
# le code d'un tiers, pour une raison impossible à deviner depuis le message.
GO_PKGS := ./cmd/... ./internal/...
GO_DIRS := cmd internal

# Registre personnel pendant les développements : les images du dashboard ne
# vont pas dans `cnous-mse` tant que le projet n'est pas livrable.
IMAGE_REPO ?= reg.cadoles.com/vcarroy/mse-dashboard
# Le tag reprend le commit : republier sous le même tag ne suffirait pas à faire
# reprendre l'image par le cluster, les pods n'imposant pas d'`imagePullPolicy`.
IMAGE_TAG  ?= dev-$(shell git rev-parse --short HEAD 2>/dev/null || echo inconnue)
IMAGE      := $(IMAGE_REPO):$(IMAGE_TAG)

# Tag consommé par l'overlay local. Distinct de IMAGE_TAG : en local l'image est
# injectée directement dans les nœuds kind, sans passer par un registre.
LOCAL_IMAGE := $(IMAGE_REPO):dev
KIND_CLUSTER ?= mse
OVERLAY ?= deploy/overlays/local

PG_TEST_NAME := pg-mse-dashboard-test
PG_TEST_PORT := 15432
PG_TEST_DSN  := postgres://postgres:t3st@127.0.0.1:$(PG_TEST_PORT)/mse?sslmode=disable

MINIO_TEST_NAME   := minio-mse-dashboard-test
MINIO_TEST_PORT   := 19000
MINIO_TEST_BUCKET := mse-dashboard-test

.PHONY: help build test test-integration test-ci lint fmt fmt-check vet vuln clean check dev run-once \
        web web-install web-dev web-check dev-prereqs image image-push kind-load manifests deploy undeploy logs

help: ## Affiche cette aide
	@grep -hE '^[a-z-]+:.*##' $(MAKEFILE_LIST) | sed -E 's/:.*## /\t/' | expand -t24

build: ## Compile le binaire
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags='-s -w' -o $(BINARY) ./cmd/mse-dashboard

test: ## Exécute les tests (les tests d'intégration sont ignorés sans TEST_DATABASE_URL)
	$(GO) test $(GO_PKGS)

# -p 1 sérialise les paquets : les tests de `store` réinitialisent le schéma, et
# rien ne garantirait l'ordre avec ceux qui en dépendent.
# -count=1 désactive le cache : un test d'intégration « cached » n'a rien vérifié.
test-integration: ## Tests avec un PostgreSQL et un MinIO jetables
	@docker rm -f $(PG_TEST_NAME) $(MINIO_TEST_NAME) >/dev/null 2>&1 || true
	@docker run -d --name $(PG_TEST_NAME) -e POSTGRES_PASSWORD=t3st -e POSTGRES_DB=mse \
		-p $(PG_TEST_PORT):5432 postgres:17-alpine >/dev/null
	@docker run -d --name $(MINIO_TEST_NAME) -p $(MINIO_TEST_PORT):9000 \
		-e MINIO_ROOT_USER=minioadmin -e MINIO_ROOT_PASSWORD=minioadmin \
		quay.io/minio/minio:latest server /data >/dev/null
	@# Le bucket est créé par le harnais de test lui-même : le faire ici
	@# obligerait chaque environnement d'exécution à le refaire à sa façon.
	@until curl -sf http://127.0.0.1:$(MINIO_TEST_PORT)/minio/health/live >/dev/null 2>&1; do sleep 0.5; done
	@# pg_isready répond « prêt » sur le serveur temporaire d'initdb, avant que le
	@# serveur définitif n'accepte les connexions TCP. Attendre une vraie requête
	@# sur la base cible est le seul signal fiable.
	@until docker exec $(PG_TEST_NAME) psql -U postgres -d mse -c 'select 1' >/dev/null 2>&1; do sleep 0.5; done
	@TEST_DATABASE_URL=$(PG_TEST_DSN) \
		TEST_MINIO_ENDPOINT=127.0.0.1:$(MINIO_TEST_PORT) \
		TEST_MINIO_BUCKET=$(MINIO_TEST_BUCKET) \
		$(GO) test -p 1 -count=1 $(GO_PKGS) ; status=$$? ; \
		docker rm -f $(PG_TEST_NAME) $(MINIO_TEST_NAME) >/dev/null 2>&1 ; exit $$status

# Variante pour un environnement qui fournit déjà PostgreSQL et MinIO — services
# de l'intégration continue, ou conteneurs démarrés à la main. Les drapeaux sont
# ainsi définis à un seul endroit : la CI et le poste exécutent la même chose.
test-ci: ## Tests d'intégration contre des services déjà démarrés
	$(GO) test -p 1 -count=1 $(GO_PKGS)

fmt: ## Reformate le code
	$(GO) fmt $(GO_PKGS)

# `go fmt` réécrit les fichiers, donc réussit toujours : l'employer en intégration
# continue laisserait passer du code mal formaté après l'avoir corrigé dans un
# arbre de travail jeté à la fin du job. La vérification doit constater, pas
# corriger.
fmt-check: ## Échoue si un fichier n'est pas formaté
	@unformatted=$$(gofmt -l $(GO_DIRS)) ; \
	if [ -n "$$unformatted" ] ; then \
		echo "fichiers non formatés (lancer 'make fmt') :" ; \
		echo "$$unformatted" ; \
		exit 1 ; \
	fi

vet: ## Analyse statique du toolchain
	$(GO) vet $(GO_PKGS)

lint: fmt-check vet ## Vérification du formatage + analyse statique

vuln: ## Recherche de vulnérabilités dans les dépendances
	# Passe par un script plutôt que d'appeler govulncheck directement : celui-ci
	# n'offre aucun mécanisme d'exclusion, et une vulnérabilité sans correctif
	# amont rendrait le job rouge pour toujours. Les exceptions sont déclarées et
	# justifiées dans le script, réaffichées à chaque exécution, et une exception
	# devenue inutile y fait échouer la vérification.
	./scripts/vulncheck.sh $(GO_PKGS)

# Cible unique reproduisant ce que la CI vérifie : un développeur doit pouvoir
# constater localement l'échec qu'il obtiendrait en intégration. La promesse ne
# tient que si aucune des cibles appelées ne modifie l'arbre de travail.
check: lint test web-check ## Ce que la CI vérifie

clean: ## Supprime les artefacts de build
	rm -rf bin/

# --- Interface ----------------------------------------------------------------

WEB := apps/web

web-install: ## Installe les dépendances de l'interface
	npm --prefix $(WEB) ci --no-audit --no-fund

# Vite écrit directement dans internal/web/dist, d'où `//go:embed` le lit : il
# n'y a pas d'étape de copie, donc pas de moyen de l'oublier.
web: ## Construit l'interface dans internal/web/dist
	npm --prefix $(WEB) run build

web-dev: ## Sert l'interface en développement, API relayée vers :8080
	npm --prefix $(WEB) run dev

web-check: ## Typage, analyse statique et tests de l'interface
	npm --prefix $(WEB) run typecheck
	npm --prefix $(WEB) run lint
	npm --prefix $(WEB) run test

# --- Environnement de développement -------------------------------------------

# CRD Flux et objets de développement, sans aucun contrôleur. Le CRD PortalData
# appartient au dépôt `mse` et n'est pas copié ici : la cible échoue en indiquant
# comment l'installer plutôt que d'en embarquer une copie qui divergerait.
dev-prereqs: ## Installe les CRD Flux et les objets Flux de développement
	./hack/dev-prereqs/install-crds.sh

# --- Image et déploiement -----------------------------------------------------

# Le secret Git est monté comme le fait Skaffold, et pour la même raison : le
# module `tekton-mse-manager` vit dans un dépôt privé, et `go mod download` ne
# peut pas s'authentifier sans lui. Son absence ici rendait cette cible — donc
# `make deploy`, le seul chemin de déploiement sans Skaffold — inutilisable :
# elle échouait sur « could not read Username for forge.cadoles.com ».
#
# Un secret BuildKit ne laisse aucune trace dans les couches de l'image,
# contrairement à un `COPY` qui les y graverait définitivement.
image: ## Construit l'image, taguée par le commit et `dev`
	DOCKER_BUILDKIT=1 docker build \
		--secret id=git-credentials,src=$(HOME)/.git-credentials \
		--build-arg VERSION=$(IMAGE_TAG) \
		--build-arg REVISION=$$(git rev-parse HEAD 2>/dev/null || echo inconnue) \
		-t $(IMAGE) -t $(LOCAL_IMAGE) .

image-push: image ## Pousse l'image sur le registre
	docker push $(IMAGE)

# Injection directe dans les nœuds kind : évite l'aller-retour par le registre,
# qui domine largement le temps d'une itération locale.
kind-load: image ## Charge l'image dans le cluster kind
	kind load docker-image $(LOCAL_IMAGE) --name $(KIND_CLUSTER)

manifests: ## Rend et analyse les manifests de l'overlay
	@kustomize build $(OVERLAY) > /dev/null
	@kustomize build $(OVERLAY) | kube-linter lint -

# Les Job sont supprimés avant d'appliquer : `spec.template` d'un Job est
# immuable, et un simple `apply` échouerait sur « field is immutable » dès que
# le tag d'image ou une variable de configuration change.
# Conservée pour un déploiement ponctuel sans Skaffold. Préférer `make dev` :
# le tag `dev` étant mutable, cette cible doit forcer un redémarrage, et l'oubli
# de ce redémarrage donne un cluster qui tourne sur du code ancien en affichant
# des manifests à jour.
deploy: kind-load manifests ## Déploie sur kind (overlay local, sans Skaffold)
	@# Un Job est immuable : l'`apply` qui suit échoue sur « field is immutable »
	@# tant que le précédent n'a pas disparu. `--wait` seul n'a pas suffi — la
	@# cible a échoué puis réussi sans autre changement que la relance — d'où
	@# l'attente explicite de la disparition, qui ne se contente pas de la
	@# demande de suppression.
	kubectl delete job -n mse-dashboard mse-dashboard-migrate mse-dashboard-minio-bucket \
		--ignore-not-found --wait=true
	kubectl wait --for=delete -n mse-dashboard --timeout=60s \
		job/mse-dashboard-migrate job/mse-dashboard-minio-bucket 2>/dev/null || true
	kustomize build $(OVERLAY) | kubectl apply -f -
	kubectl wait --for=condition=complete --timeout=180s -n mse-dashboard job/mse-dashboard-migrate
	@# Le tag `dev` ne change pas d'un build à l'autre : sans redémarrage forcé,
	@# les pods garderaient l'image déjà présente sur le nœud.
	kubectl rollout restart -n mse-dashboard deployment/mse-dashboard-api deployment/mse-dashboard-worker
	kubectl rollout status  -n mse-dashboard deployment/mse-dashboard-api --timeout=180s
	kubectl rollout status  -n mse-dashboard deployment/mse-dashboard-worker --timeout=180s

undeploy: ## Supprime le déploiement (le namespace emporte les PVC)
	kustomize build $(OVERLAY) | kubectl delete --ignore-not-found -f -

logs: ## Suit les journaux de l'api
	kubectl logs -n mse-dashboard -l app.kubernetes.io/component=api --tail=100 -f

# --- Boucle de développement -------------------------------------------------

# `--cleanup=false` sur les deux cibles, et ce n'est pas un détail de confort.
#
# Skaffold nettoie par défaut quand il est interrompu **ou qu'il échoue** : il
# supprime ce qu'il a déployé, namespace compris, et le namespace emporte les
# PVC. Un échec de chargement d'image dans kind — qui arrive, `ctr` refusant
# parfois une image déjà présente — détruisait donc PostgreSQL et MinIO.
#
# Ce qui disparaît alors n'est pas rattrapable : les runs et les versions se
# reconstruisent depuis Tekton et Redmine, mais les **releases**, les **jobs** et
# le **journal d'audit** n'existent que dans cette base. C'est arrivé.
#
# Pour repartir de zéro délibérément : `make undeploy`.
dev: ## Boucle Skaffold : reconstruit et redéploie à chaque modification
	skaffold dev --cleanup=false

run-once: ## Construit et déploie une fois via Skaffold
	skaffold run --cleanup=false

SECRETS_DIR := deploy/overlays/local/components/pipeline-credentials

.PHONY: pipeline-credentials
pipeline-credentials: ## Prépare les identifiants que les pipelines utiliseront (local)
	@# Fabrique les trois fichiers que l'overlay local monte en secrets, dans le
	@# namespace des PipelineRun. Aucun n'est versionné.
	@#
	@# Ils sont distincts de ceux de `mse-tekton`, qui visent l'application
	@# réelle : la clé y est une clé de déploiement de `CNOUS/mse`, et le compte
	@# de registre n'a `push` que sur `cnous-mse`.
	@set -eu; \
	mkdir -p $(SECRETS_DIR); \
	if [ ! -f $(SECRETS_DIR)/pipeline-ssh-key ]; then \
	  echo '==> génération d une paire de clés dédiée'; \
	  ssh-keygen -t ed25519 -C mse-dashboard-pipelines -N '' \
	    -f $(SECRETS_DIR)/pipeline-ssh-key >/dev/null; \
	  mv $(SECRETS_DIR)/pipeline-ssh-key.pub $(SECRETS_DIR)/pipeline-ssh-key.pub.txt; \
	else \
	  echo '==> clé déjà présente, conservée'; \
	fi; \
	if [ ! -f $(SECRETS_DIR)/pipeline-known-hosts ]; then \
	  echo '==> relevé de la clé d hôte de la forge'; \
	  ssh-keyscan -p $${FORGE_SSH_PORT:-2222} $${FORGE_HOST:-forge.cadoles.com} \
	    > $(SECRETS_DIR)/pipeline-known-hosts 2>/dev/null; \
	fi; \
	if [ ! -f $(SECRETS_DIR)/pipeline-dockerconfig.json ]; then \
	  echo '==> identifiants de registre'; \
	  printf 'Serveur [https://reg.cadoles.com] : '; read -r server; \
	  server=$${server:-https://reg.cadoles.com}; \
	  printf 'Utilisateur : '; read -r user; \
	  printf 'Mot de passe : '; stty -echo; read -r pass; stty echo; echo; \
	  kubectl create secret docker-registry tmp-regcred \
	    --docker-server="$$server" --docker-username="$$user" --docker-password="$$pass" \
	    --dry-run=client -o jsonpath='{.data.\.dockerconfigjson}' \
	    | base64 -d > $(SECRETS_DIR)/pipeline-dockerconfig.json; \
	  unset pass; \
	fi; \
	chmod 600 $(SECRETS_DIR)/pipeline-ssh-key $(SECRETS_DIR)/pipeline-dockerconfig.json; \
	echo; \
	echo 'Il reste deux gestes, sur vos comptes :'; \
	echo; \
	echo '  1. Déclarer cette clé publique en LECTURE SEULE sur le dépôt visé'; \
	echo '     (Gitea -> dépôt -> Paramètres -> Clés de déploiement) :'; \
	echo; \
	sed 's/^/     /' $(SECRETS_DIR)/pipeline-ssh-key.pub.txt; \
	echo; \
	echo '  2. Donner au compte de registre le droit d écrire sur le projet visé'; \
	echo '     (Harbor -> projet -> Membres -> rôle Developer).'; \
	echo; \
	echo 'Puis activer ces secrets dans dashboard.env :'; \
	echo '     PIPELINE_GIT_SECRET=mse-dashboard-pipeline-git'; \
	echo '     PIPELINE_REGISTRY_SECRET=mse-dashboard-pipeline-registry'
