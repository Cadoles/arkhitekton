-- +goose Up
-- Rattache à leur release les rapports ingérés orphelins.

-- Les trois ingestions — tests, charge, sécurité — laissaient `release_id` nul :
-- `LoadRun` porte bien le champ, et les appels ne le renseignaient pas. Les
-- rapports s'enregistraient donc correctement, mais l'écran d'une release les
-- cherche par `release_id` et n'en trouvait aucun. Le score restait « non
-- évalué » sur une release dont les tests avaient tourné et réussi.
--
-- Le lien manquant est déductible : un rapport porte son run, un run porte son
-- job, et un job porte sa release. La réparation est donc exacte, et non une
-- approximation — c'est pourquoi elle se fait ici plutôt qu'en demandant de
-- relancer les pipelines.
--
-- `security_audits` n'a pas de colonne propre : la table se joint à
-- `test_results`, et se répare donc du même coup.
update test_results t
   set release_id = j.release_id
  from runs r
  join jobs j on j.id = r.job_id
 where r.id = t.run_id
   -- Seuls les orphelins. Un rapport déjà rattaché ne doit pas changer de
   -- release parce que son job aurait été réaffecté depuis.
   and t.release_id is null
   and j.release_id is not null;

-- +goose Down
-- Irréversible à dessein : distinguer un rattachement rendu par cette migration
-- d'un rattachement écrit à l'ingestion demanderait de retenir lesquels, et
-- annuler la réparation reviendrait à recréer le défaut.
select 1;
