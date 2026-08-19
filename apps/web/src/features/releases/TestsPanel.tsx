import { Link } from 'react-router'
import { CollapsibleSection } from '../../components/CollapsibleSection'
import { Icon } from '../../components/Icon'
import { EmptyState, Skeleton, StatusBadge } from '../../components/primitives'
import { status } from '../../lib/status'
import { HealthGauge } from './HealthGauge'
import { SignalGrid } from './SignalGrid'
import { FAMILY_LABEL, SIGNAL_LABEL, useTests, type ScoreBreakdown } from './tests'

/*
 * Score de qualité et résultats de tests d'une release.
 *
 * Le score est toujours affiché **avec sa décomposition**. Un score dont on ne
 * peut pas voir le détail n'est pas ajustable en connaissance de cause : « 72 »
 * ne dit pas s'il faut corriger des tests ou des vulnérabilités (B11).
 *
 * La disposition est celle de la maquette : la jauge à gauche, les signaux en
 * tuiles à droite, le tout dans une seule carte. C'est le bloc qu'on lit en
 * premier, et le seul qui doive tenir dans un coup d'œil.
 */
export function TestsPanel({ tag }: { tag: string }) {
  const { data, isPending } = useTests(tag)

  if (isPending) return <Skeleton className="h-48 w-full" />
  if (!data) return null

  return (
    <>
      <ScoreCard tag={tag} score={data.score} />

      <CollapsibleSection title="Résultats de tests" count={data.results.length}>
        {data.results.length === 0 ? (
          <EmptyState>Aucun résultat de test pour cette release.</EmptyState>
        ) : (
          <ul className="flex flex-col gap-2">
            {data.results.map((result) => (
              <li
                key={result.id}
                className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border border-zinc-200 px-3 py-2 text-sm"
              >
                <Link
                  to={
                    result.family === 'security'
                      ? `/releases/${encodeURIComponent(tag)}/security`
                      : `/releases/${encodeURIComponent(tag)}/tests/${encodeURIComponent(result.family)}`
                  }
                  className="font-medium text-zinc-900 hover:underline"
                >
                  {FAMILY_LABEL[result.family] ?? result.family}
                </Link>
                <span className="text-zinc-500">{result.tool}</span>
                <StatusBadge style={status.pipeline(result.status)} />
                <span className="text-zinc-500">{result.summary}</span>
                {result.flaky > 0 && (
                  <span className="flex items-center gap-1 text-warn-700">
                    <Icon name="alert" className="size-3.5" />
                    {result.flaky} instable(s)
                  </span>
                )}
              </li>
            ))}
          </ul>
        )}
      </CollapsibleSection>
    </>
  )
}

function ScoreCard({ tag, score }: { tag: string; score: ScoreBreakdown }) {
  return (
    <section className="rounded-lg border border-zinc-200 bg-white p-4">
      <div className="grid gap-4 md:grid-cols-[auto_1fr] md:items-center">
        <HealthGauge score={score.value} health={score.measured ? score.health : 'na'} />

        <div className="flex flex-col gap-3">
          {!score.measured && (
            // Zéro serait un jugement, et le plus sévère possible sur une release
            // qu'on n'a pas regardée.
            <p className="text-sm text-zinc-500">
              Aucun signal n'a été mesuré pour cette release : il n'y a pas de score, ce qui n'est
              pas la même chose qu'un score de zéro.
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

          <SignalGrid tag={tag} signals={score.signals} />
        </div>
      </div>
    </section>
  )
}
