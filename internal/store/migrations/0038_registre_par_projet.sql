-- Laisse un projet désigner le secret de registre qu'il emploie.
--
-- `image_registry` est déjà par projet depuis la migration 0021 : un dépôt peut
-- publier vers `reg.cadoles.com/cnous-mse` pendant qu'un autre vise ailleurs. Le
-- **secret** qui autorise à y pousser, lui, était resté global —
-- `PIPELINE_REGISTRY_SECRET`, une seule valeur pour tous. Publier ailleurs
-- demandait donc des identifiants que ce secret-là ne porte pas, et le run
-- échouait au `push` après avoir construit l'image.
--
-- # Désigner, et non créer
--
-- La table de propriété de doc 12 §12.7 confie ce secret au dashboard, comme la
-- clé de déploiement. Ce n'est pas ce qui est fait ici, et l'écart est délibéré.
--
-- Une clé de déploiement, le dashboard l'**engendre** : aucune matière ne vient
-- d'un humain, rien ne traverse le navigateur, et la moitié privée naît dans le
-- processus qui l'écrit. Un identifiant de registre vient d'un humain. Le faire
-- entrer dans un Secret depuis l'interface le ferait transiter par le navigateur
-- et l'API — l'exposition que §12.7 nomme lui-même comme « réelle » — et l'API
-- tourne sans jeton de cluster, délibérément, parce que c'est la surface
-- exposée. Les deux seules façons de l'y faire arriver seraient de lui accorder
-- ce jeton, ou de garder l'identifiant en base : la première défait la décision
-- du lot G, la seconde viole la règle « jamais en base » de §12.7.
--
-- Le projet **désigne** donc un secret que le déploiement provisionne — par
-- ExternalSecret adossé à Vault en production, ce que §12.7 exige de garder comme
-- chemin de premier rang. C'est la règle de doc 07 §7.3 appliquée sans
-- aménagement : le dashboard désigne, il ne distribue pas.
--
-- Conséquence pratique, et elle est bonne : le secret désigné est déjà déclaré
-- montable par le déploiement, comme `regcred` l'est. Aucune entrée à ajouter à
-- la liste des secrets montables, donc aucun nom étranger à y faire entrer — ce
-- que la garantie structurelle du lot G interdit précisément.

-- +goose Up
alter table projects add column if not exists registry_secret text not null default '';

-- Vide vaut « celui du déploiement ». Un défaut nul aurait obligé chaque lecteur
-- à se garder, et un défaut nommé aurait figé ici une valeur qui appartient à la
-- configuration.

-- +goose Down
alter table projects drop column if exists registry_secret;
