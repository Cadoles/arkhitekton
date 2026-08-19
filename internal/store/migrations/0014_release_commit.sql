-- +goose Up
-- Le commit d'une release, distinct de la référence choisie à sa création.
--
-- `git_ref` porte ce qu'un humain a désigné — `main`, `develop`, un tag — et
-- reste utile pour dire d'où la release est partie. Mais une branche bouge : deux
-- reconstructions de la même release, à six mois d'écart, produiraient des images
-- différentes en croyant viser le même point.
--
-- `git_commit` est la résolution de cette référence à l'instant de la création.
-- C'est lui qui rend une release reproductible, et c'est le premier maillon de
-- l'invariant de version (B17) : commit → tag Git → tag d'image → version.txt.
--
-- Nullable : les releases créées avant cette migration n'ont pas de commit connu,
-- et en inventer un serait pire que d'assumer le trou.
alter table releases add column git_commit text;

comment on column releases.git_commit is
    'SHA du commit construit, résolu depuis git_ref à la création. Nul pour les releases antérieures.';

-- +goose Down
alter table releases drop column if exists git_commit;
