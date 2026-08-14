import { Icon } from '../../components/Icon'
import { Sparkline } from '../../components/Sparkline'
import { Card, EmptyState, Skeleton, StatusBadge } from '../../components/primitives'
import { formatDateTime } from '../../lib/format'
import { status } from '../../lib/status'
import { hasAtLeast, useSession } from '../auth/session'
import { useBench, usePinBaseline, type BenchMeasure, type Campaign } from './bench'

/*
 * Mesures de charge d'une release.
 *
 * L'écran porte une distinction que la maquette ne pouvait pas prévoir : une
 * mesure peut être **valide mais non comparable**. Le cluster qui héberge Tekton
 * est aussi celui de la cible ; un build qui tourne pendant la campagne la fausse
 * (B16). Ces mesures sont affichées — les cacher ferait croire que la campagne
 * n'a pas eu lieu — mais sans verdict, et le motif est dit.
 */
export function BenchPanel({ tag }: { tag: string }) {
  const { data, isPending } = useBench(tag)

  if (isPending) return <Skeleton className="h-40 w-full" />
  if (!data) return null

  return (
    <Card title="Tests de charge">
      {data.campaigns.length === 0 ? (
        <EmptyState>Aucune campagne de charge pour cette release.</EmptyState>
      ) : (
        <div className="flex flex-col gap-4">
          {data.campaigns.map((campaign) => (
            <CampaignBlock key={campaign.id} tag={tag} campaign={campaign} />
          ))}
        </div>
      )}
    </Card>
  )
}

function CampaignBlock({ tag, campaign }: { tag: string; campaign: Campaign }) {
  const session = useSession()
  const canPin = session.kind === 'authenticated' && hasAtLeast(session.identity.role, 'releaser')

  return (
    <section className="rounded-lg border border-zinc-200 p-3">
      <header className="mb-2 flex flex-wrap items-center gap-x-3 gap-y-1">
        <h3 className="text-sm font-semibold text-zinc-900">{campaign.scenario}</h3>
        <StatusBadge style={status.bench(campaign.verdict)} />
        <span className="text-sm text-zinc-500">{formatDateTime(campaign.measured_at)}</span>
        {campaign.summary && <span className="text-sm text-zinc-500">{campaign.summary}</span>}
      </header>

      {!campaign.comparable && (
        // Sans ce bandeau, l'absence de verdict se lit comme une panne de mesure,
        // alors que c'est une mesure valide dont on refuse la comparaison.
        <p className="mb-2 flex items-start gap-2 rounded-md border border-warn-500 bg-warn-50 px-3 py-2 text-sm text-warn-700">
          <Icon name="alert" className="mt-0.5 size-4 shrink-0" />
          <span>
            Mesure non comparable : {campaign.reason}. Les valeurs sont conservées, mais elles n'entrent ni dans le
            verdict ni dans la tendance.
          </span>
        </p>
      )}

      <ul className="flex flex-col gap-1">
        {campaign.measures.map((measure) => (
          <MeasureRow
            key={measure.id}
            tag={tag}
            measure={measure}
            history={campaign.history?.[measure.metric] ?? []}
            canPin={canPin && campaign.comparable}
          />
        ))}
      </ul>
    </section>
  )
}

function MeasureRow({
  tag,
  measure,
  history,
  canPin,
}: {
  tag: string
  measure: BenchMeasure
  history: number[]
  canPin: boolean
}) {
  const pin = usePinBaseline(tag)

  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-zinc-200 py-1.5 text-sm first:border-t-0">
      <span className="min-w-32 text-zinc-500">{measure.label}</span>
      <span className="font-mono font-medium text-zinc-900">
        {format(measure.value)} {measure.unit}
      </span>

      {measure.baseline !== undefined ? (
        <span className="text-zinc-500">
          réf. <span className="font-mono">{format(measure.baseline)}</span>
          {measure.baseline_at && ` du ${formatDateTime(measure.baseline_at)}`}
        </span>
      ) : (
        <span className="text-zinc-500">aucune référence</span>
      )}

      {measure.verdict !== 'na' && <StatusBadge style={status.bench(measure.verdict)} />}

      {/* Le verdict de k6 sur ses propres seuils, distinct de la comparaison à la
          baseline. Les deux peuvent se contredire : un scénario peut tenir son
          contrat tout en s'étant dégradé depuis la référence. */}
      {measure.threshold_ok === false && (
        <span className="flex items-center gap-1 text-critical-700">
          <Icon name="cross" className="size-3.5" />
          seuil k6 non tenu
        </span>
      )}

      <Sparkline points={history} lowerBetter={measure.lower_better} className="ml-auto" />

      {canPin && (
        <button
          type="button"
          onClick={() => pin.mutate(measure.id)}
          disabled={pin.isPending}
          className="rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-50"
          title="Désigner cette mesure comme référence pour toutes les comparaisons suivantes"
        >
          Épingler
        </button>
      )}
    </li>
  )
}

function format(value: number): string {
  if (Number.isInteger(value)) return value.toLocaleString('fr-FR')
  return value.toLocaleString('fr-FR', { maximumFractionDigits: 2 })
}
