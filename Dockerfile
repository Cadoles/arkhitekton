# Image du dashboard : un binaire statique, trois rôles (api, worker, migrate).
#
# Le même artefact sert les trois : c'est ce qui garantit qu'un `migrate` exécuté
# en Job applique exactement le schéma qu'attend l'`api` déployé juste après. Deux
# images distinctes autoriseraient un décalage, et ce décalage se manifesterait en
# erreur SQL au premier appel plutôt qu'au déploiement.

# L'interface est construite dans l'image, et non sur le poste : le binaire
# embarque la SPA, et un `dist/` périmé traîné depuis un arbre de travail
# produirait une image dont l'interface et l'API ne correspondent pas.
FROM node:22-alpine AS web

# L'arborescence du dépôt est reproduite : la sortie de Vite est configurée en
# relatif (`../../internal/web/dist`), et un WORKDIR à plat la ferait atterrir
# hors de tout chemin prévisible.
WORKDIR /src/apps/web

# Les dépendances d'abord, pour la même raison que côté Go.
COPY apps/web/package.json apps/web/package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY apps/web/ ./
RUN npm run build

FROM golang:1.26-alpine AS build

WORKDIR /src

# `git` est indispensable depuis que le dashboard dépend d'un module privé.
#
# Un module public arrive en archive depuis proxy.golang.org, et aucun outil
# n'est requis. Un module privé est récupéré en `direct`, c'est-à-dire par un
# clone git — et l'image Go alpine n'embarque pas git. Sans lui, la construction
# échoue sur « executable file not found in $PATH », message qui ne dit rien de
# la cause réelle.
RUN apk add --no-cache git

# `forge.cadoles.com` n'est pas servi par le proxy public, et sa somme de
# contrôle n'est pas dans sum.golang.org. Sans GOPRIVATE, Go interrogerait l'un
# et l'autre, qui répondraient tous deux que le module n'existe pas.
ENV GOPRIVATE=forge.cadoles.com/*

# Les dépendances d'abord : elles changent rarement, le code souvent. Sans cette
# séparation, chaque modification d'un fichier .go relancerait un téléchargement
# complet du module cache.
COPY go.mod go.sum ./

# Le secret n'est monté que le temps de cette instruction et ne laisse aucune
# trace dans les couches de l'image — un `COPY` des identifiants les y graverait
# définitivement, y compris après suppression dans une couche ultérieure.
#
# En local, Skaffold monte `~/.git-credentials`. En intégration, il faudra un
# jeton en lecture sur le dépôt du module : c'est la même exigence d'accès, et
# elle n'est pas encore satisfaite côté CI.
RUN --mount=type=secret,id=git-credentials,target=/root/.git-credentials \
    git config --global credential.helper store && go mod download

COPY . .

# Après le COPY, sans quoi le contexte écraserait le résultat du build frontend.
COPY --from=web /src/internal/web/dist ./internal/web/dist

ARG VERSION=dev
ARG REVISION=inconnue

# CGO désactivé : le binaire doit tourner dans une image sans libc. `-trimpath`
# ôte les chemins de la machine de build, qui n'apprennent rien et varient d'un
# build à l'autre.
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.revision=${REVISION}" \
      -o /out/mse-dashboard ./cmd/mse-dashboard

# distroless static : pas de shell, pas de gestionnaire de paquets, rien à
# exploiter en cas d'exécution de commande arbitraire. Elle apporte les deux
# seules choses dont le binaire a besoin : les certificats racine (découverte
# OIDC en HTTPS, MinIO en TLS) et un utilisateur non privilégié déclaré.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/mse-dashboard /usr/local/bin/mse-dashboard

# 65532 = `nonroot` dans l'image distroless. La valeur est répétée dans le
# securityContext des manifests : `runAsNonRoot` seul ne suffit pas, le kubelet
# doit pouvoir vérifier l'UID sans résoudre l'utilisateur de l'image.
USER 65532:65532

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/mse-dashboard"]
CMD ["api"]
