-- +goose Up
-- Supprime les rôles d'amorçage jamais employés.
--
-- L'amorçage insérait au démarrage une ligne par identifiant déclaré, en prenant
-- chacun pour un `subject`. Or Gitea met son identifiant numérique dans le claim
-- `sub` — `121` — là où un humain écrit `vcarroy` dans un fichier de
-- configuration. La ligne créée ne correspondait donc à aucun utilisateur, et
-- l'utilisateur réel se connectait sans aucune permission.
--
-- Ces lignes ne sont pas seulement inutiles : elles accordent le rôle
-- administrateur à quiconque obtiendrait un jour ce `subject`. Sur un
-- fournisseur qui dérive le sujet du nom de compte, ce n'est pas hypothétique.
--
-- La sélection est étroite à dessein. `granted_by = 'bootstrap'` restreint aux
-- lignes produites par ce mécanisme, et `last_seen is null` à celles dont
-- personne ne s'est jamais servi — signature exacte du fantôme. Un administrateur
-- d'amorçage qui s'est réellement connecté garde son rôle.
delete from user_roles
where granted_by = 'bootstrap'
  and last_seen is null;

-- +goose Down
-- Rien à défaire : ces lignes n'accordaient de droit à personne, et les
-- recréer supposerait de connaître les identifiants d'alors.
select 1;
