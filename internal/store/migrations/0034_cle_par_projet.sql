-- Donne à chaque projet sa propre clé de déploiement.
--
-- Une seule clé ouvrait tous les dépôts déclarés — `PIPELINE_GIT_SECRET`,
-- posée par le déploiement et désignée à tous les pipelines. À l'échelle d'un
-- produit cela ne tient pas : une clé compromise se révoque projet par projet,
-- et déclarer un projet ne devrait pas lui donner l'accès à ceux qui l'ont
-- précédé (doc 12 §12.7, lot G).
--
-- La colonne ne porte que la **moitié publique**. C'est la seule que le
-- dashboard conserve, et elle est publique au sens propre : c'est ce qu'on colle
-- dans les réglages d'un dépôt. La moitié privée vit dans un Secret du namespace
-- des pipelines, que le dashboard écrit une fois et ne relit jamais — sa RBAC
-- lui accorde `create`, `patch` et `delete` sans `get` ni `list`.
--
-- **Cette colonne est aussi le registre de propriété.** Faute de pouvoir lire les
-- Secrets, le dashboard ne peut pas demander au cluster si celui qui porte le nom
-- d'un projet lui appartient. Il le lit ici : renseignée, la clé vient de lui et
-- une rotation peut remplacer le Secret ; vide, un Secret homonyme vient
-- d'ailleurs et il refuse d'y toucher.

-- +goose Up
alter table projects add column if not exists deploy_key_public text not null default '';

-- La date de pose accompagne la clé : « renseigné le … » est ce que l'écran
-- affiche, puisque la valeur, elle, ne se réaffiche pas. Sans elle, une clé
-- ancienne et une clé de la veille seraient indiscernables au moment de décider
-- d'une rotation.
alter table projects add column if not exists deploy_key_created_at timestamptz;

-- Les deux vont ensemble ou pas du tout : une date sans clé décrirait une pose
-- qui n'a rien laissé, une clé sans date une pose dont on ignore quand elle a eu
-- lieu.
alter table projects add constraint projects_deploy_key_coherent
  check ((deploy_key_public = '') = (deploy_key_created_at is null));

-- +goose Down
alter table projects drop constraint if exists projects_deploy_key_coherent;
alter table projects drop column if exists deploy_key_created_at;
alter table projects drop column if exists deploy_key_public;
