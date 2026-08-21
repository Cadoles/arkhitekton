import { Icon } from '../../components/Icon'
import { Skeleton } from '../../components/primitives'
import { HealthGauge } from './HealthGauge'
import { SignalGrid, Spinner } from './SignalGrid'
import { buildIsRunning } from './signals'
import { SIGNAL_LABEL, useTests, type ScoreBreakdown } from './tests'
import type { ReleaseJob } from './api'

/*
 * Score de qualité d'une release.
 *
 * Le score est toujours affiché **avec sa décomposition**. Un score dont on ne
 * peut pas voir le détail n'est pas ajustable en connaissance de cause : « 72 »
 * ne dit pas s'il faut corriger des tests ou des vulnérabilités (B11).
 *
 * La disposition est celle de la maquette : la jauge à gauche, les signaux en
 * tuiles à droite, le tout dans une seule carte. C'est le bloc qu'on lit en
 * premier, et le seul qui doive tenir dans un coup d'œil.
 *
 * La liste des rapports bruts a été retirée. Elle empilait l'historique de toutes
 * les exécutions — trois lignes par construction, sans jamais rien remplacer — et
 * redisait ce que les tuiles portent déjà. Le détail d'une famille vit sur son
 * écran, atteignable depuis sa tuile.
 */
export function TestsPanel({
  tag,
  jobs = [],
}: {
  tag: string
  /** Exécutions de la release, d'où l'écran déduit ce qui est en cours.
   *
   *  Absentes sur l'écran d'un environnement, qui montre le score de la release
   *  déployée sans en charger le détail : rien n'y est annoncé comme en cours,
   *  ce qui est exact — il n'y a pas d'action à y attendre. */
  jobs?: ReleaseJob[]
}) {
  const { data, isPending } = useTests(tag)

  if (isPending) return <Skeleton className="h-48 w-full" />
  if (!data) return null

  return <ScoreCard tag={tag} score={data.score} jobs={jobs} />
}

function ScoreCard({
  tag,
  score,
  jobs,
}: {
  tag: string
  score: ScoreBreakdown
  jobs: ReleaseJob[]
}) {
  const building = buildIsRunning(jobs)

  return (
    <section className="rounded-lg border border-zinc-200 bg-white p-4">
      <div className="grid gap-4 md:grid-cols-[auto_1fr] md:items-center">
        <HealthGauge
          score={score.value}
          health={score.measured ? score.health : 'na'}
          pending={building}
        />

        <div className="flex flex-col gap-3">
          {building && (
            // Pendant une construction, aucun signal ne décrit ce qui se
            // construit : les tests qui suivront porteront sur une autre image.
            // Laisser le score sans mention le ferait lire comme actuel.
            <p className="flex items-center gap-2 rounded-md border border-zinc-200 px-3 py-2 text-sm text-zinc-600">
              <Spinner className="size-4 shrink-0 text-zinc-400" />
              Construction en cours. Le score affiché est celui de l&apos;image précédente : les
              vérifications repartiront sur la nouvelle.
            </p>
          )}

          {!score.measured && !building && (
            // Zéro serait un jugement, et le plus sévère possible sur une release
            // qu'on n'a pas regardée.
            <p className="text-sm text-zinc-500">
              Aucun signal n&apos;a été mesuré pour cette release : il n&apos;y a pas de score, ce
              qui n&apos;est pas la même chose qu&apos;un score de zéro.
            </p>
          )}

          {score.redistributed.length > 0 && score.measured && (
            // Un 100 sur trois signaux ne doit pas ressembler à un 100 sur six.
            <p className="flex items-start gap-2 rounded-md border border-zinc-200 px-3 py-2 text-sm text-zinc-600">
              <Icon name="alert" className="mt-0.5 size-4 shrink-0 text-warn-700" />
              <span>
                Score calculé sur {score.signals.length - score.redistributed.length} signal(aux)
                sur {score.signals.length}. Non mesurés :{' '}
                {score.redistributed.map((signal) => SIGNAL_LABEL[signal] ?? signal).join(', ')}.
                Leur poids a été redistribué sur le reste.
              </span>
            </p>
          )}

          <SignalGrid tag={tag} signals={score.signals} jobs={jobs} />
        </div>
      </div>
    </section>
  )
}
