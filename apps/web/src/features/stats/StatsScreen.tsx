import { useState } from 'react'
import { Card, EmptyState, ErrorState, Skeleton, StatusBadge } from '../../components/primitives'
import { currentProject, projects } from '../../lib/project'
import { status, type ReleaseState } from '../../lib/status'
import { useStats, type Stats, type StatsBucket, type StatsRange, type StatsScope } from './api'

/*
 * Les statistiques : combien de releases ont été lancées, et comment se comportent les pipelines,
 * sur une période. Pour le projet courant, ou pour tous les projets où l'on est opérateur.
 */

const RANGES: { value: StatsRange; label: string }[] = [
  { value: '30d', label: '30 jours' },
  { value: '90d', label: '90 jours' },
  { value: '12m', label: '12 mois' },
]

export function StatsScreen() {
  const [scope, setScope] = useState<StatsScope>('project')
  const [range, setRange] = useState<StatsRange>('30d')
  const { data, isPending, error, refetch } = useStats(scope, range)
  const current = projects().find((project) => project.slug === currentProject())

  const scopes: { value: StatsScope; label: string }[] = [
    { value: 'project', label: current?.label ?? 'Ce projet' },
    { value: 'all', label: 'Tous mes projets' },
  ]

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-2xl font-semibold text-zinc-900">Statistiques</h1>
        <div className="flex flex-wrap gap-3">
          <Toggle label="Portée" options={scopes} value={scope} onChange={setScope} />
          <Toggle label="Période" options={RANGES} value={range} onChange={setRange} />
        </div>
      </div>

      {isPending && <Skeleton className="h-64 w-full" />}
      {error && <ErrorState message="Les statistiques n'ont pas pu être chargées." onRetry={() => void refetch()} />}

      {data &&
        (data.projects.length === 0 ? (
          <Card>
            <EmptyState>Aucun projet où vous êtes opérateur.</EmptyState>
          </Card>
        ) : (
          <>
            {scope === 'all' && (
              <p className="text-sm text-zinc-500">{data.projects.map((project) => project.label).join(' · ')}</p>
            )}
            <Releases stats={data} />
            <Pipelines stats={data} />
          </>
        ))}
    </div>
  )
}

function Toggle<T extends string>({
  label,
  options,
  value,
  onChange,
}: {
  label: string
  options: { value: T; label: string }[]
  value: T
  onChange: (value: T) => void
}) {
  return (
    <div className="flex gap-1" role="group" aria-label={label}>
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          onClick={() => onChange(option.value)}
          aria-pressed={value === option.value}
          className={`rounded-md border px-3 py-1 text-sm font-medium transition-colors ${
            value === option.value
              ? 'border-zinc-900 bg-zinc-900 text-white'
              : 'border-zinc-300 bg-white text-zinc-600 hover:bg-zinc-100'
          }`}
        >
          {option.label}
        </button>
      ))}
    </div>
  )
}

function Releases({ stats }: { stats: Stats }) {
  const { releases } = stats
  return (
    <Card title="Releases lancées">
      {releases.total === 0 ? (
        <EmptyState>Aucune release lancée sur cette période.</EmptyState>
      ) : (
        <div className="flex flex-col gap-4">
          <div className="flex flex-wrap items-center gap-x-6 gap-y-2">
            <p className="text-3xl font-semibold text-zinc-900 tabular-nums">{releases.total}</p>
            <div className="flex flex-wrap gap-1.5" aria-label="Par état">
              {releases.by_state.map((entry) => (
                <span key={entry.key} className="inline-flex items-center gap-1">
                  <StatusBadge style={status.release(entry.key as ReleaseState) ?? fallbackStyle(entry.key)} />
                  <span className="text-sm text-zinc-700 tabular-nums">{entry.count}</span>
                </span>
              ))}
            </div>
            <div className="flex flex-wrap gap-1.5 text-sm text-zinc-600" aria-label="Par type">
              {releases.by_kind.map((entry) => (
                <span key={entry.key} className="rounded border border-zinc-200 px-1.5 py-0.5">
                  {entry.label || entry.key} <span className="tabular-nums">{entry.count}</span>
                </span>
              ))}
            </div>
          </div>
          <Chart buckets={releases.buckets} bucket={stats.bucket} />
        </div>
      )}
    </Card>
  )
}

function fallbackStyle(state: string) {
  return { label: state, icon: 'minus' as const, text: 'text-zinc-600', bg: 'bg-zinc-50', border: 'border-zinc-300' }
}

const WEEK = new Intl.DateTimeFormat('fr-FR', { day: 'numeric', month: 'short', timeZone: 'UTC' })
const MONTH = new Intl.DateTimeFormat('fr-FR', { month: 'short', year: '2-digit', timeZone: 'UTC' })

/** Une barre par semaine ou par mois : les releases validées, en échec, et les autres. */
function Chart({ buckets, bucket }: { buckets: StatsBucket[]; bucket: Stats['bucket'] }) {
  const highest = Math.max(1, ...buckets.map((entry) => entry.count))
  const format = bucket === 'week' ? WEEK : MONTH
  return (
    <figure className="flex flex-col gap-2">
      <ol className="flex h-40 items-end gap-1" aria-label={bucket === 'week' ? 'Par semaine' : 'Par mois'}>
        {buckets.map((entry) => {
          const released = entry.by_state.released ?? 0
          const failed = entry.by_state.failed ?? 0
          const other = entry.count - released - failed
          const label = `${bucket === 'week' ? 'Semaine du ' : ''}${format.format(new Date(entry.start))} : ${entry.count}`
          return (
            <li
              key={entry.start}
              className="flex h-full min-w-0 flex-1 flex-col items-center justify-end gap-1"
              title={label}
            >
              {entry.count > 0 && <span className="text-xs text-zinc-600 tabular-nums">{entry.count}</span>}
              <div
                className="flex w-full max-w-10 flex-col-reverse overflow-hidden rounded-t"
                style={{ height: `${(entry.count / highest) * 100}%` }}
                aria-label={label}
              >
                <div className="bg-healthy-500" style={{ flexGrow: released }} />
                <div className="bg-critical-500" style={{ flexGrow: failed }} />
                <div className="bg-zinc-300" style={{ flexGrow: other }} />
              </div>
            </li>
          )
        })}
      </ol>
      <ol className="flex gap-1" aria-hidden="true">
        {buckets.map((entry) => (
          <li key={entry.start} className="min-w-0 flex-1 truncate text-center text-[10px] text-zinc-500">
            {format.format(new Date(entry.start))}
          </li>
        ))}
      </ol>
      <figcaption className="flex flex-wrap gap-3 text-xs text-zinc-500">
        <Legend tone="bg-healthy-500" label="Validées" />
        <Legend tone="bg-critical-500" label="En échec" />
        <Legend tone="bg-zinc-300" label="Autres" />
      </figcaption>
    </figure>
  )
}

function Legend({ tone, label }: { tone: string; label: string }) {
  return (
    <span className="inline-flex items-center gap-1">
      <span className={`size-2.5 rounded-sm ${tone}`} aria-hidden="true" />
      {label}
    </span>
  )
}

function Pipelines({ stats }: { stats: Stats }) {
  return (
    <Card title="Pipelines terminés">
      {stats.pipelines.length === 0 ? (
        <EmptyState>Aucun pipeline terminé sur cette période.</EmptyState>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-zinc-200 text-left text-xs text-zinc-500">
              <th className="py-1.5 pr-3 font-medium">Pipeline</th>
              <th className="py-1.5 pr-3 text-right font-medium">Exécutions</th>
              <th className="py-1.5 pr-3 text-right font-medium">Échecs</th>
              <th className="py-1.5 text-right font-medium">Durée médiane</th>
            </tr>
          </thead>
          <tbody>
            {stats.pipelines.map((pipeline) => {
              const rate = pipeline.runs > 0 ? Math.round((pipeline.failed / pipeline.runs) * 100) : 0
              return (
                <tr key={pipeline.kind} className="border-b border-zinc-100 last:border-0">
                  <td className="py-1.5 pr-3 text-zinc-900" title={pipeline.kind}>
                    {pipeline.label}
                  </td>
                  <td className="py-1.5 pr-3 text-right tabular-nums text-zinc-700">{pipeline.runs}</td>
                  <td
                    className={`py-1.5 pr-3 text-right tabular-nums ${pipeline.failed > 0 ? 'text-critical-700' : 'text-zinc-500'}`}
                  >
                    {pipeline.failed} ({rate} %)
                  </td>
                  <td className="py-1.5 text-right tabular-nums text-zinc-700">
                    {pipeline.median_seconds === undefined ? 'non mesurée' : duration(pipeline.median_seconds)}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      )}
    </Card>
  )
}

/** Une durée lisible : « 45 s », « 3 min 12 s », « 1 h 05 min ». */
export function duration(seconds: number): string {
  const total = Math.round(seconds)
  if (total < 60) return `${total} s`
  const minutes = Math.floor(total / 60)
  if (minutes < 60) return `${minutes} min ${String(total % 60).padStart(2, '0')} s`
  return `${Math.floor(minutes / 60)} h ${String(minutes % 60).padStart(2, '0')} min`
}
