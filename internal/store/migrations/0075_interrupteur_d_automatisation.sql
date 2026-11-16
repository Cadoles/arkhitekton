-- L'automatisation d'une release s'éteint sans être vidée.
--
-- Vider le plan disait déjà « ce projet ne construit plus », mais perdait ce qu'il construisait.
-- L'interrupteur garde le plan : une release déclarée pendant qu'il est éteint ne lance rien, et le
-- rallumer rend l'automatisation telle qu'elle était. Les lignes existantes restent allumées.

-- +goose Up
alter table release_automations add column if not exists enabled boolean not null default true;

-- +goose Down
alter table release_automations drop column if exists enabled;
