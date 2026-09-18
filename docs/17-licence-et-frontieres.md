# 17 — Licence et frontières

Arkhitekton passe sous **GNU AGPL v3 ou ultérieure**. Ce document dit pourquoi, et
surtout ce que ce choix fait aux deux endroits où le projet attend des contributions
qu'il ne contrôlera pas : les plugins et les pipelines.

Jusqu'ici le dépôt n'avait aucune licence — donc, par défaut, tous droits réservés :
personne d'autre que le titulaire n'avait le droit de s'en servir.

## 17.1 Pourquoi l'AGPL et pas la GPL

Arkhitekton n'est presque jamais *distribué*. Il est **exposé** : une instance tourne
dans un cluster, et ses utilisateurs s'en servent par le réseau sans jamais en recevoir
une copie. Sous GPL, cette exploitation ne déclenche aucune obligation — le code
modifié peut rester chez celui qui l'héberge indéfiniment.

La section 13 de l'AGPL est exactement ce trou-là refermé : qui offre le programme à des
utilisateurs distants leur doit les sources de ce qu'il fait tourner, modifications
comprises. Pour un outil dont la valeur est d'être installé et adapté chez chacun, c'est
la clause qui fait la différence entre un commun et une base de départ pour des forks
privés.

## 17.2 Ce que l'AGPL nue ferait aux plugins

Un plugin Arkhitekton n'est pas un processus voisin. C'est un **paquet Go compilé dans le
binaire** : `internal/plugin` tient un registre rempli à la compilation,
`cmd/arkhitekton/plugins.go` assemble, et le noyau ne connaît jamais que des descripteurs
(`internal/plugin/boundary_test.go` interdit au noyau d'importer `internal/mse`).

Cette frontière est réelle pour l'architecture — elle garantit qu'une instance sans le
plugin MSE reste cohérente — mais elle ne dit rien au droit d'auteur. Le lien est
statique : un seul binaire, un seul programme. Sous AGPL nue, un plugin tiers serait
couvert par l'AGPL, et son auteur n'aurait pas le choix de sa licence.

**Aucun choix d'architecture ne change cela tant que le lien est à la compilation.**
Seules deux voies l'ouvrent : une exception explicite à la licence, ou un plugin
hors-processus — gRPC, WASM, un exécutable appelé — où la séparation est reconnue.
La première est retenue ; la seconde reste possible plus tard sans rien invalider.

## 17.3 L'exception, et ce qu'elle ne fait pas

`LICENSE.plugin-exception` accorde une permission additionnelle au titre de la
**section 7** de l'AGPL. Elle définit une *interface de plugin* — l'API Go désignée, l'API
HTTP, et les formats déclaratifs lus par le programme — puis autorise qui écrit une œuvre
n'employant que cette interface à la publier sous la licence de son choix, et à
distribuer **ou exposer en réseau** l'ensemble dans ces conditions.

Le « ou exposer en réseau » n'est pas décoratif : sans lui, l'exception ne servirait à
rien ici. Un plugin fermé serait libre d'être distribué mais rattrapé par la section 13
dès qu'une instance le sert — c'est-à-dire dans le seul mode d'emploi réel du produit.

Trois limites, délibérées :

- **Le noyau reste sous AGPL.** Modifier Arkhitekton pour accueillir un plugin, c'est
  modifier Arkhitekton : ces changements sont dus. L'exception ne couvre que le plugin.
- **Elle ne s'étend pas à un fork.** Qui copie du code du noyau dans son plugin sort de
  la définition et retombe sous l'AGPL.
- **Un destinataire peut la retirer de sa copie**, comme la section 7 le prévoit. Cela
  n'affecte aucune autre copie.

## 17.4 Ce qu'il reste à faire pour qu'un plugin tiers existe

L'exception est écrite avant que le besoin soit atteignable, et c'est voulu : une licence
se pose au début, pas quand la première contribution extérieure frappe à la porte.

Aujourd'hui, **un plugin tiers est techniquement impossible** : `internal/plugin` est sous
`internal/`, et Go interdit son import depuis un autre module. Trois choses manquent,
dans cet ordre :

1. **Sortir l'API désignée de `internal/`** — un paquet `plugin/` à la racine du module —
   en gardant le test de frontière qui empêche le noyau d'importer un plugin nommément.
2. **Figer ce qui est promis.** Une interface publiée est un contrat : les descripteurs,
   les types d'événements, les actions. Ce que doc 12 et doc 14 décrivent déjà informe ce
   contrat ; il faudra dire ce qui est stable et ce qui ne l'est pas.
3. **Trancher le mode de chargement.** À la compilation, l'auteur du plugin livre un
   binaire reconstruit — l'exception est précisément ce qui rend cette livraison possible
   sous sa propre licence. Hors-processus, la question de licence devient secondaire, mais
   le coût d'exécution et la surface d'API augmentent.

Quand l'API changera de chemin, `LICENSE.plugin-exception` devra le suivre : elle nomme
le paquet désigné « à la date de ce fichier ».

## 17.5 Les pipelines sont des données

Un pipeline est du YAML lu par Tekton, un catalogue du YAML lu par Arkhitekton. Ni l'un
ni l'autre n'est lié au programme : les écrire ne crée pas d'œuvre dérivée, et l'AGPL ne
leur impose rien. Doc 13 décrit le contrat qu'ils honorent ; c'est un contrat de format,
pas un lien de code.

Le vrai risque était ailleurs. **On n'écrit pas un pipeline depuis une page blanche** : on
part de `task-git-clone.yaml` ou de `pipeline-build-image.yaml` et on les adapte. Livrés
sous AGPL, ces huit fichiers auraient contaminé les pipelines dérivés — le contraire de
ce que le contrat cherche à permettre.

Ils sont donc sous **Apache 2.0** (`deploy/base/pipelines/LICENSE`), licence de Tekton
lui-même, explicite sur les brevets. `deploy/base/config/catalog.yaml` suit, comme modèle
de catalogue.

## 17.6 Décisions

**Titulaire : Cadoles.** Une seule ligne de copyright, pas une liste : le jour où le
projet accepte des contributions extérieures, il faudra décider d'un DCO ou d'un CLA —
question ouverte, hors de ce document.

**AGPL-3.0-or-later**, et non `-only` : sans le « ou ultérieure », toute adoption d'une
future version exigerait l'accord de chaque contributeur.

**Apache 2.0 pour les pipelines livrés**, pas CC0 : l'attribution reste due et la clause
de brevets est explicite, ce qui vaut mieux pour du code d'infrastructure copié tel quel.

**Pas d'en-tête SPDX dans les ~210 fichiers source.** Le `LICENSE` racine, ce document et
la section du README suffisent à établir le régime ; des en-têtes partout ajouteraient du
bruit à chaque diff sans rien lever d'ambigu. Les deux endroits où deux régimes se
touchent — `deploy/base/pipelines/` et `catalog.yaml` — sont marqués, eux.

**Ce document n'est pas un avis juridique.** Le texte de l'exception mérite une relecture
par un juriste avant la première publication extérieure ; c'est le seul passage du dépôt
dont la formulation, et non le comportement, engage.
