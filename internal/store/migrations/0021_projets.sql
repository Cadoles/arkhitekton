-- +goose Up
-- Les dépôts que le dashboard pilote, et ce qu'il sait en construire.

-- Ces dépôts étaient écrits dans la configuration : `FORGE_MSE_REPO_URL`,
-- `FORGE_MSE_KUSTOM_REPO_URL`, `FORGE_TEKTON_REPO_URL`, `MSE_DOCKERFILE`. Trois
-- conséquences.
--
-- En déclarer un de plus demandait de modifier un ConfigMap, donc de reconstruire
-- et de redéployer. Rien ne disait, depuis l'interface, quels dépôts le dashboard
-- croyait piloter. Et surtout, rien ne permettait de vérifier qu'il y avait
-- réellement accès : l'écart ne se découvrait qu'au clone, en cours de pipeline,
-- sur un message qui ne désignait pas la cause.
--
-- La table ne remplace pas encore la configuration : elle décrit, elle ne pilote
-- pas. Les pipelines continuent de recevoir leurs dépôts par les clés de
-- configuration. Le raccordement demandera de trancher ce que l'ordonnanceur
-- accepte d'une demande — il refuse aujourd'hui qu'elle fournisse un dépôt, et
-- c'est ce qui empêche de détourner un build — et cette décision mérite d'être
-- prise seule.
create table projects (
    id bigserial primary key,

    -- Le dépôt au format `owner/nom`, tel que l'API Gitea le nomme. Clé
    -- naturelle : deux entrées pour un même dépôt décriraient la même chose deux
    -- fois, et rien ne dirait laquelle fait foi.
    repository text not null unique,

    -- Le libellé affiché. Distinct du dépôt : `CNOUS/mse` se lit « Portail MSE »
    -- sur un écran, et le nom du dépôt n'a pas à porter cette charge.
    label text not null,

    -- L'URL de la forge, sans le dépôt. Séparée pour qu'un dépôt hébergé
    -- ailleurs reste décrivable sans que la table suppose une forge unique.
    forge_url text not null,

    -- Ce que le projet sait produire. Vide : il est piloté sans être construit —
    -- `mse-kustom` et `mse-tekton` sont clonés par l'audit sans jamais donner
    -- d'image.
    dockerfile    text not null default '',
    build_context text not null default '.',

    -- `multi_stage` distingue un Dockerfile à cibles d'un Dockerfile simple. Ce
    -- n'est pas déductible de la liste des étapes : une liste vide peut vouloir
    -- dire « pas encore renseignée » aussi bien que « une seule image ».
    multi_stage boolean not null default false,

    -- Les étapes constructibles, chacune associée au dépôt d'image qu'elle
    -- alimente : [{"stage": "web", "image": "portal"}]. Deux champs et non un —
    -- la cible `cli` publie `portal-cli`, et renommer l'une ne doit pas obliger à
    -- renommer l'autre.
    targets jsonb not null default '[]'::jsonb,

    -- Constat du dernier test d'accès. Conservé plutôt que recalculé à
    -- l'affichage : l'appel sort du dashboard, et le refaire à chaque ouverture
    -- de l'écran ferait payer une latence de forge pour une information qui
    -- change une fois par trimestre.
    checked_at timestamptz,
    check_ok   boolean,
    check_note text not null default '',

    created_by text not null,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

comment on table projects is
    'Dépôts pilotés par le dashboard. Descriptif : les pipelines reçoivent encore leurs dépôts par configuration.';

-- +goose Down
drop table if exists projects;
