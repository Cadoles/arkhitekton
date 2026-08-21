-- +goose Up
-- Reconclut les releases restées en échec sur des verdicts remplacés.

-- Le décompte des vérifications cumulait les essais d'une même famille : trois
-- échecs d'intégration suivis d'une réussite comptaient trois échecs. Une
-- release relancée avec succès restait donc en échec, et le bouton « Rejouer »
-- ne rattrapait rien.
--
-- La requête est corrigée, mais l'état déjà écrit ne se recalcule pas tout
-- seul : rien ne repasse sur une release une fois conclue. Cette migration
-- rejoue la conclusion pour celles que le nouveau décompte rend décidables.
--
-- Conditions, toutes nécessaires :
--   * la release est en `failed` — les autres états n'ont rien à rattraper ;
--   * sa dernière construction a réussi — un échec de build n'est pas rattrapé
--     par des tests, il demande une reconstruction ;
--   * le dernier essai de chaque famille lancée après cette construction a
--     réussi, et il y en a au moins un.
--
-- `ready` et non `released` : la release redevient décidable, pas livrée. La
-- décision reste humaine (B3).
with derniere_construction as (
    select release_id, max(id) as job_id
      from jobs
     where kind = 'build'
     group by release_id
),
derniers_essais as (
    select distinct on (j.release_id, j.kind) j.release_id, j.kind, j.state
      from jobs j
      join derniere_construction b on b.release_id = j.release_id
     where j.kind in ('static-tests', 'security-audit', 'integration-tests')
       and j.id > b.job_id
     order by j.release_id, j.kind, j.id desc
),
verdicts as (
    select release_id,
           count(*) as familles,
           count(*) filter (where state <> 'succeeded') as non_reussies
      from derniers_essais
     group by release_id
)
update releases r
   set state = 'ready'
  from verdicts v
  join derniere_construction b on b.release_id = v.release_id
  join jobs construction on construction.id = b.job_id
 where r.id = v.release_id
   and r.state = 'failed'
   and construction.state = 'succeeded'
   and v.familles > 0
   and v.non_reussies = 0;

-- +goose Down
-- Irréversible : distinguer une release rattrapée ici d'une release passée en
-- `ready` par la voie normale demanderait de retenir lesquelles, et rétablir un
-- échec périmé reviendrait à recréer le défaut.
select 1;
