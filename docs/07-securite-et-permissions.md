# 07 — Sécurité et permissions

## 7.1 Authentification OIDC

### Modèle retenu : BFF, session côté serveur

Le backend Go porte l'intégralité du flux OIDC. Le navigateur ne voit jamais de jeton.

```
1. GET /api/v1/auth/login
   → génération state + nonce + PKCE verifier, stockés en session côté serveur
   → 302 vers l'autorisation du fournisseur

2. GET /api/v1/auth/callback?code&state
   → vérification du state, échange code → jetons (PKCE)
   → vérification de l'ID token : signature JWKS, iss, aud, exp, nonce
   → création de la session, cookie posé
   → 302 vers l'application

3. Requêtes suivantes
   → cookie de session, rien d'autre

4. GET /api/v1/auth/logout
   → destruction de la session + RP-initiated logout auprès du fournisseur
```

**Cookie de session** : `HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/`, durée alignée
sur celle du refresh token. Le contenu est un identifiant opaque ; la session vit en
base (table `sessions`), ce qui permet de la révoquer côté serveur — impossible avec un
JWT auto-porté.

**Pourquoi pas de jeton dans le navigateur.** Un jeton en `localStorage` est lisible par
tout script de la page ; en mémoire seule, il est perdu à chaque rechargement et impose
un `iframe` de renouvellement silencieux. Le dashboard déclenche des déploiements en
production : le vol de session doit être aussi difficile que possible, et la révocation
immédiate possible.

**Rafraîchissement.** Le backend renouvelle l'access token en tâche de fond avant
expiration. Si le refresh échoue, la session est invalidée et l'API répond `401` ; le
frontend redirige vers la connexion.

**CSRF.** `SameSite=Lax` couvre les requêtes intersites de navigation, mais pas tout.
Les méthodes non sûres exigent en plus un en-tête `X-CSRF-Token` dont la valeur est
liée à la session (double soumission avec cookie non-`HttpOnly` dédié).

### Fournisseur : Gitea

**Tranché.** Gitea agit comme fournisseur OIDC. Le dashboard y est déclaré comme
application OAuth2, flux code + PKCE.

Choix cohérent : les utilisateurs du dashboard sont déjà des utilisateurs de la forge,
et le `sub` du jeton correspond à un compte Gitea réel. Cela rend directement
attribuables les champs `created_by`, `deployed_by` et les entrées d'audit, et permet
de recouper une release avec l'auteur des PR qu'elle embarque.

Claims utilisées : `sub` (identité stable, clé d'autorisation), `preferred_username`,
`name`, `email` pour l'affichage. **Aucune claim de groupes n'est requise.**

À confirmer avec l'équipe qui exploite l'instance : version de Gitea et portées
disponibles, pour valider le flux code + PKCE et la présence des claims d'affichage.

### Source des rôles : table interne

**Tranché : les permissions sont gérées manuellement dans le dashboard.** Ce qui était
prévu comme repli devient le mécanisme principal.

```sql
create table user_roles (
  subject     text primary key,               -- sub du jeton Gitea
  username    text,                           -- pour l'affichage et la recherche
  role        text not null
              check (role in ('viewer','operator','releaser','admin')),
  granted_by  text not null,
  granted_at  timestamptz not null default now()
);
```

Gérée depuis l'écran `/admin` par les porteurs du rôle `admin`, tracée dans
`audit_log`.

Conséquences à traiter explicitement :

| Point | Traitement |
|---|---|
| **Amorçage** | une liste de `sub` ou de noms d'utilisateur administrateurs est fournie en configuration et appliquée au démarrage. Sans elle, personne ne peut attribuer le premier rôle |
| **Utilisateur inconnu** | authentification réussie mais aucun rôle : accès refusé avec un message explicite, et l'utilisateur apparaît dans `/admin` en attente d'attribution — plutôt qu'une erreur opaque suivie d'un ticket |
| **Rôle par défaut** | configurable. `viewer` par défaut si l'instance Gitea est déjà restreinte aux personnes concernées ; aucun rôle sinon |
| **Départs** | rien ne révoque automatiquement un rôle quand un compte Gitea est désactivé. Une revue périodique des rôles est à prévoir, et l'écran `/admin` affiche la date de dernière connexion pour la rendre praticable |

Le dernier point est le coût réel de la gestion manuelle : elle est simple à mettre en
œuvre et se désynchronise silencieusement. Le rendre visible dans l'interface est ce qui
évite qu'un rôle `releaser` survive au départ de son titulaire.

## 7.2 Autorisation

### Rôles

| Rôle | Portée |
|---|---|
| `viewer` | lecture de tout, aucune écriture |
| `operator` | + lancer des tests, créer des pré-releases, promouvoir hors production |
| `releaser` | + créer des releases finales, promouvoir en production |
| `admin` | + configuration, resynchronisations forcées, attribution des rôles |

La demande ne distingue que « readonly » et « droit de création de release ». Le
découpage en quatre rôles anticipe une distinction qui apparaît toujours en pratique :
lancer une campagne E2E sur `unstable` et déployer en production ne sont pas le même
geste et ne concernent pas les mêmes personnes. Si le client ne veut que deux rôles,
`operator` et `releaser` sont fusionnés dans la configuration — aucun changement de
code.

### Permissions

| Permission | viewer | operator | releaser | admin |
|---|:--:|:--:|:--:|:--:|
| `read:*` | ● | ● | ● | ● |
| `pipeline:run` | | ● | ● | ● |
| `pipeline:cancel` | | ● | ● | ● |
| `release:create` (pre) | | ● | ● | ● |
| `release:create` (final, patch) | | | ● | ● |
| `release:edit` | | ● | ● | ● |
| `release:export` | ● | ● | ● | ● |
| `deploy:unstable`, `deploy:qualif` | | ● | ● | ● |
| `deploy:preprod` | | | ● | ● |
| `deploy:prod` | | | ● | ● |
| `admin:*` | | | | ● |

Les permissions de déploiement sont indexées par environnement, cohérent avec le fait
que les environnements sont une table de configuration
([B13](02-blocages-et-decisions.md#b13)) : ajouter un environnement ne demande pas de
modifier le modèle de permissions.

### Mise en œuvre

L'autorisation est vérifiée dans un middleware, à partir d'une table
`route → permission` déclarative, et **non** par des tests dispersés dans les handlers.
Un test parcourt automatiquement toutes les routes déclarées dans `openapi.yaml` et
échoue si l'une d'elles n'a pas de permission associée — c'est ce qui évite qu'une
route ajoutée plus tard soit accessible sans contrôle.

Le frontend masque les actions interdites, mais ce masquage est du confort. Toute
décision d'autorisation est prise côté serveur, sans exception.

## 7.3 RBAC Kubernetes

`ServiceAccount` dédié `mse-dashboard` dans le namespace `mse-dashboard`, sans aucun
`ClusterRole` d'écriture.

### Namespace `mse-tekton-pipelines`

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: mse-dashboard-tekton
  namespace: mse-tekton-pipelines
rules:
  # Lecture des définitions : nécessaire pour valider le catalogue au démarrage
  - apiGroups: ["tekton.dev"]
    resources: ["pipelines", "tasks"]
    verbs: ["get", "list", "watch"]
  # Pilotage des exécutions
  - apiGroups: ["tekton.dev"]
    resources: ["pipelineruns"]
    verbs: ["get", "list", "watch", "create", "patch", "delete"]
  - apiGroups: ["tekton.dev"]
    resources: ["taskruns"]
    verbs: ["get", "list", "watch"]
  # Collecte des logs
  - apiGroups: [""]
    resources: ["pods", "pods/log"]
    verbs: ["get", "list", "watch"]
  # Diagnostic d'un run bloqué (PVC non lié, image non tirée)
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["get", "list", "watch"]
```

`patch` sur `pipelineruns` sert uniquement à poser `spec.status: Cancelled`.
`delete` sert au nettoyage des runs créés par le dashboard.

Absents volontairement : `secrets` (le dashboard n'a pas à lire les secrets Tekton),
`pods/exec` (aucun besoin d'exécuter dans un pod), toute écriture sur `pipelines` et
`tasks` (elles appartiennent à `mse-tekton`).

### Namespaces d'environnement

Une `Role` en lecture stricte par namespace applicatif :

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: mse-dashboard-observer
  namespace: <namespace de l'environnement>
rules:
  - apiGroups: ["apps"]
    resources: ["deployments", "statefulsets", "replicasets"]
    verbs: ["get", "list", "watch"]
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "watch"]
```

Suffisant pour lire les images et les digests réellement en cours d'exécution
([B8](02-blocages-et-decisions.md#b8)) sans donner le moindre droit d'écriture sur les
environnements. Si un redémarrage contrôlé après re-tag s'avère nécessaire (voir
[B7](02-blocages-et-decisions.md#b7)), il passe par un pipeline Tekton dédié utilisant
le compte `tekton-kubectl` existant, et non par un élargissement de ces droits.

### Élection de leader

```yaml
- apiGroups: ["coordination.k8s.io"]
  resources: ["leases"]
  verbs: ["get", "create", "update"]
```
dans le namespace `mse-dashboard` uniquement.

## 7.4 Gestion des secrets

Le dashboard consomme : clé d'API Redmine, jeton Gitea, jeton GitLab, identifiants
registre, identifiants MinIO, secret client OIDC, mot de passe PostgreSQL.

**Tous** sont fournis par `ExternalSecret` (`external-secrets.io` est déjà déployé dans
le cluster) et injectés en variables d'environnement ou en fichiers montés. Aucun secret
n'est versionné, ni en clair, ni encodé en base64 dans un manifeste.

Ce point mérite d'être posé explicitement parce que le dépôt `mse-tekton` fait
aujourd'hui l'inverse ([B10](02-blocages-et-decisions.md#b10)) : il versionne en clair
une clé privée SSH de déploiement, un mot de passe de service MinIO, une URL de webhook
Rocket.Chat et des identifiants de comptes de test. Le dashboard ne doit pas reprendre
ce schéma, et les identifiants concernés — en particulier le compte MinIO qui a un accès
en écriture au bucket servant de stockage de preuve — devraient être renouvelés avant la
mise en production. Il s'agit d'une remarque sur l'existant, pas d'un blocage pour le
développement.

### URLs pré-signées

La décision [B14](02-blocages-et-decisions.md#b14) fait du dashboard l'émetteur d'URLs
`PUT` pré-signées, remises aux pipelines en paramètre. C'est un gain net : le secret
`minio-credentials` sort de la quasi-totalité des Tasks, et il n'y a plus de credential
MinIO permanent dans les pipelines.

Une URL pré-signée est cependant une **capacité portant un droit d'écriture**. Quatre
règles :

| Règle | Détail |
|---|---|
| Portée minimale | une URL par objet, méthode `PUT` uniquement, clé exacte — jamais un préfixe |
| Expiration | alignée sur le timeout du pipeline concerné, jamais au-delà (90 min pour `security-audit`, 45 min pour l'intégration) |
| Non-journalisation | le paramètre `upload-urls` est masqué par le middleware de journalisation et absent de l'audit, au même titre qu'un secret |
| Compte dédié | les URLs sont signées avec un compte de service en écriture seule sur le préfixe `raw/` du bucket `mse-dashboard`, distinct du compte de lecture utilisé par l'API |

**Écart constaté.** Le compte `svc-mse-pipeline` employé aujourd'hui par les pipelines lit
et écrit **l'intégralité** du bucket — vérifié lors du test SigV4 du 13/08/2026. Signer
des URLs avec ce compte donnerait à chaque URL la portée du compte si la clé fuitait.
Créer le compte restreint est donc un prérequis de mise en œuvre de
[B14](02-blocages-et-decisions.md#b14), pas une amélioration ultérieure.

Les URLs apparaissent en clair dans le `spec.params` du `PipelineRun`, donc lisibles par
qui a `get` sur les `PipelineRun` du namespace. C'est acceptable au regard de leur
portée — écriture sur une clé unique, expirant avec le run — mais cela justifie de ne
jamais y mettre autre chose qu'un `PUT` sur un objet précis. En particulier : pas d'URL
`GET` pré-signée sur des artefacts existants, pas d'URL couvrant un préfixe.

## 7.5 Durcissement applicatif

| Mesure | Détail |
|---|---|
| Conteneur | non-root, `readOnlyRootFilesystem: true`, toutes les capabilities supprimées, `seccompProfile: RuntimeDefault` |
| En-têtes | `Content-Security-Policy` sans `unsafe-inline`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin`, HSTS |
| Limitation de débit | par session sur les routes d'écriture, par IP sur les routes d'authentification |
| Validation | tout corps de requête validé contre le schéma OpenAPI avant traitement |
| Journalisation | JSON structuré, jamais de jeton ni de secret ; les identifiants d'utilisateur sont journalisés (nécessaire à l'audit) |
| Dépendances | `govulncheck` et `npm audit` en intégration continue, échec bloquant sur les vulnérabilités hautes |
| Images | le dashboard s'audite lui-même : son image passe par le pipeline `security-audit` existant |

## 7.6 Traçabilité

Toute action modifiant l'état — création de release, mise en file, annulation,
déploiement, édition de note, validation d'action manuelle — écrit dans `audit_log` :
acteur, action, cible, paramètres, horodatage.

Cette table est consultable dans l'interface par les rôles `releaser` et `admin`, et
exportable. Le déclenchement d'un déploiement en production depuis une interface web
n'est acceptable qu'à cette condition.
