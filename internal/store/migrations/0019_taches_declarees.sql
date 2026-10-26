-- +goose Up
-- Les tâches qu'un pipeline déclare, et pas seulement celles qui ont démarré.

-- L'écran d'une exécution ne listait que les TaskRun existants : Tekton n'en crée
-- une que lorsque la tâche démarre. Un pipeline de huit étapes en montrait donc
-- deux au début, et la frise laissait croire qu'il n'y en avait pas d'autres. On
-- ne pouvait pas savoir ce qui restait à faire.
--
-- Tekton résout le pipeline à la création du run et en dépose la définition dans
-- `status.pipelineSpec` : la liste complète est donc connue dès le départ, sans
-- second appel au cluster. Elle est projetée ici pour que l'API la rende sans
-- avoir à parler à Kubernetes — ce que le processus qui sert l'interface ne fait
-- pas.
--
-- L'ordre du tableau est celui de la déclaration, `finally` en fin : c'est
-- l'ordre du graphe, plus parlant que celui des démarrages pour ce qui n'a pas
-- encore commencé.
alter table runs
    add column declared_tasks jsonb not null default '[]'::jsonb;

comment on column runs.declared_tasks is
    'Tâches déclarées par le pipeline, dans l''ordre du graphe. Sert à afficher celles qui n''ont pas encore démarré.';

-- +goose Down
alter table runs drop column if exists declared_tasks;
