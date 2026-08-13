BINARY := bin/mse-dashboard
GO     ?= go

PG_TEST_NAME := pg-mse-dashboard-test
PG_TEST_PORT := 15432
PG_TEST_DSN  := postgres://postgres:t3st@127.0.0.1:$(PG_TEST_PORT)/mse?sslmode=disable

MINIO_TEST_NAME   := minio-mse-dashboard-test
MINIO_TEST_PORT   := 19000
MINIO_TEST_BUCKET := mse-dashboard-test

.PHONY: help build test test-integration lint fmt vet vuln clean check

help: ## Affiche cette aide
	@grep -hE '^[a-z-]+:.*##' $(MAKEFILE_LIST) | sed -E 's/:.*## /\t/' | expand -t24

build: ## Compile le binaire
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags='-s -w' -o $(BINARY) ./cmd/mse-dashboard

test: ## Exécute les tests (les tests d'intégration sont ignorés sans TEST_DATABASE_URL)
	$(GO) test ./...

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
	@until curl -sf http://127.0.0.1:$(MINIO_TEST_PORT)/minio/health/live >/dev/null 2>&1; do sleep 0.5; done
	@docker exec $(MINIO_TEST_NAME) mc alias set t http://127.0.0.1:9000 minioadmin minioadmin >/dev/null 2>&1 || true
	@docker exec $(MINIO_TEST_NAME) mc mb --ignore-existing t/$(MINIO_TEST_BUCKET) >/dev/null 2>&1 || true
	@# pg_isready répond « prêt » sur le serveur temporaire d'initdb, avant que le
	@# serveur définitif n'accepte les connexions TCP. Attendre une vraie requête
	@# sur la base cible est le seul signal fiable.
	@until docker exec $(PG_TEST_NAME) psql -U postgres -d mse -c 'select 1' >/dev/null 2>&1; do sleep 0.5; done
	@TEST_DATABASE_URL=$(PG_TEST_DSN) \
		TEST_MINIO_ENDPOINT=127.0.0.1:$(MINIO_TEST_PORT) \
		TEST_MINIO_BUCKET=$(MINIO_TEST_BUCKET) \
		$(GO) test -p 1 -count=1 ./... ; status=$$? ; \
		docker rm -f $(PG_TEST_NAME) $(MINIO_TEST_NAME) >/dev/null 2>&1 ; exit $$status

fmt: ## Formate le code
	$(GO) fmt ./...

vet: ## Analyse statique du toolchain
	$(GO) vet ./...

lint: fmt vet ## Formatage + analyse statique

vuln: ## Recherche de vulnérabilités dans les dépendances
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

# Cible unique reproduisant ce que la CI vérifie : un développeur doit pouvoir
# constater localement l'échec qu'il obtiendrait en intégration.
check: lint test ## Ce que la CI vérifie

clean: ## Supprime les artefacts de build
	rm -rf bin/
