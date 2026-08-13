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

# Les dépendances d'abord : elles changent rarement, le code souvent. Sans cette
# séparation, chaque modification d'un fichier .go relancerait un téléchargement
# complet du module cache.
COPY go.mod go.sum ./
RUN go mod download

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
