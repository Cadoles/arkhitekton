import { Link } from 'react-router'
import { Icon } from '../../components/Icon'
import { Card, EmptyState, Skeleton, StatusBadge } from '../../components/primitives'
import { status } from '../../lib/status'
import { FAMILY_LABEL, SIGNAL_LABEL, useTests, type ScoreBreakdown } from './tests'

/*
 * Score de qualité et résultats de tests d'une release.
 *
 * Le score est toujours affiché **avec sa décomposition**. Un score dont on ne
 * peut pas voir le détail n'est pas ajustable en connaissance de cause : « 72 »
 * ne dit pas s'il faut corriger des tests ou des vulnérabilités (B11).
 */
export function TestsPanel({ tag }: { tag: string }) {
  const { data, isPending } = useTests(tag)

  if (isPending) return <Skeleton className="h-48 w-full" />
  if (!data) return null

  return (
    <>
      <ScoreCard score={data.score} />

      <Card title="Résultats de tests">
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
                  to={`/releases/${encodeURIComponent(tag)}/tests/${encodeURIComponent(result.family)}`}
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
      </Card>
    </>
  )
}

function ScoreCard({ score }: { score: ScoreBreakdown }) {
  return (
    <Card
      title="Score de qualité"
      action={
        score.measured ? (
          <span className="flex items-center gap-2">
            <span className="text-2xl font-semibold text-zinc-900">{score.value}</span>
            <StatusBadge style={status.health(score.health === 'na' ? 'warn' : score.health)} />
          </span>
        ) : (
          <span className="text-sm text-zinc-500">Non évalué</span>
        )
      }
    >
      {!score.measured && (
        // Zéro serait un jugement, et le plus sévère possible sur une release
        // qu'on n'a pas regardée.
        <p className="mb-3 text-sm text-zinc-500">
          Aucun signal n'a été mesuré pour cette release : il n'y a pas de score, ce qui n'est pas la même chose qu'un
          score de zéro.
        </p>
      )}

      {score.redistributed.length > 0 && score.measured && (
        // Un 100 sur trois signaux ne doit pas ressembler à un 100 sur six.
        <p className="mb-3 flex items-start gap-2 rounded-md border border-zinc-200 px-3 py-2 text-sm text-zinc-600">
          <Icon name="alert" className="mt-0.5 size-4 shrink-0 text-warn-700" />
          <span>
            Score calculé sur {score.signals.length - score.redistributed.length} signal(aux) sur {score.signals.length}.
            Non mesurés : {score.redistributed.map((signal) => SIGNAL_LABEL[signal] ?? signal).join(', ')}. Leur poids a
            été redistribué sur le reste.
          </span>
        </p>
      )}

      <ul className="flex flex-col gap-1">
        {score.signals.map((signal) => (
          <li key={signal.signal} className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-zinc-200 py-1.5 text-sm first:border-t-0">
            <span className="min-w-40 text-zinc-500">{SIGNAL_LABEL[signal.signal] ?? signal.signal}</span>
            {signal.measured ? (
              <>
                <span className="font-mono font-medium text-zinc-900">{Math.round(signal.value)}</span>
                {/* Le poids effectif, et non le poids déclaré : c'est lui qui
                    explique la contribution réelle. */}
                <span className="text-zinc-500">poids {Math.round(signal.weight)} %</span>
              </>
            ) : (
              <span className="text-zinc-500">non mesuré</span>
            )}
            {signal.detail && <span className="ml-auto text-zinc-500">{signal.detail}</span>}
          </li>
        ))}
      </ul>
    </Card>
  )
}
