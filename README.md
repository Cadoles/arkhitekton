# Arkhitekton

Dashboard de pilotage des releases. Il s'intercale entre le suivi de projet (Redmine),
la forge (Gitea), les pipelines (Tekton) et le registre d'images, pour donner une vue
unique de l'état d'une version et dérouler une release sans quitter l'interface.

Un binaire unique, trois rôles : `api`, `worker`, `migrate`. Une interface React servie
par le même binaire. Ce qui est propre à un métier vit dans un **plugin** ; ce qu'un
pipeline fait vit dans un **catalogue** déclaratif, pas dans le code.

## Démarrer

    make help              # tous les gestes disponibles
    make check             # ce que la CI vérifie
    make test-integration  # tests avec un PostgreSQL et un MinIO jetables
    make dev               # boucle Skaffold sur un cluster kind

L'environnement de développement complet — cluster kind, simulation de Flux, registre de
test et leurs limites — est décrit dans [docs/11](docs/11-environnement-de-developpement.md).

## Documentation

La conception est écrite avant le code et tenue à jour avec lui :
[docs/README.md](docs/README.md) en donne le sommaire.

## Licence

Arkhitekton est publié sous **GNU AGPL v3 ou ultérieure** ([LICENSE](LICENSE)). Une
instance exposée en réseau doit donc offrir ses sources à ceux qui s'en servent, ses
modifications comprises.

Deux frontières échappent à cette réciprocité, pour que le projet reste ouvert à des
extensions qu'il ne contrôle pas :

- **Les plugins.** Une permission additionnelle au titre de la section 7 de l'AGPL
  ([LICENSE.plugin-exception](LICENSE.plugin-exception)) autorise un plugin qui n'emploie
  que l'interface de plugin à être publié sous la licence que son auteur choisit, et
  l'ensemble à être distribué ou exposé dans ces conditions.
- **Les pipelines et les catalogues.** Ce sont des données lues par Tekton, pas du code
  lié : les écrire ne crée pas d'œuvre dérivée. Les exemples livrés
  (`deploy/base/pipelines/`, `deploy/base/config/catalog.yaml`) sont sous **Apache 2.0**,
  pour être copiés et adaptés sans contrainte.

Le raisonnement, et ce qu'il reste à faire pour qu'un plugin tiers soit techniquement
possible, sont dans [docs/17](docs/17-licence-et-frontieres.md).

Copyright (C) 2026 Cadoles.
