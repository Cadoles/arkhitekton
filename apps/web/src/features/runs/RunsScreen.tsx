import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { Icon } from '../../components/Icon'
import { Card, EmptyState, ErrorState, Skeleton, StatusBadge } from '../../components/primitives'
import { formatDateTime } from '../../lib/format'
import { duration, formatDuration, useRuns, type Run, type RunStatus } from './api'
import { runStatus } from './status'
import { useRunEvents } from './useRunEvents'

/*
 * Liste des exécutions.
 *
 * Elle montre **tous** les runs du namespace, y compris ceux que le dashboard n'a
 * pas lancés — webhook Gitea, CronJob. C'est le premier usage réellement utile du
 * dashboard : voir ce que les pipelines ont fait, avec leurs journaux, y compris
 * après disparition des pods.
 */

const FILTERS: { value: 'all' | 'running' | 'failed'; label: string }[] = [
  { value: 'all', label: 'Toutes' },
  { value: 'running', label: 'En cours' },
  { value: 'failed', label: 'En échec' },
]

function matches(run: Run, filter: string, search: string): boolean {
  if (filter === 'running' && (run.status === 'succeeded' || run.status === 'failed' || run.status === 'cancelled' || run.status === 'timedout')) {
    return false
  }
  if (filter === 'failed' && run.status !== 'failed' && run.status !== 'timedout') return false

  if (search) {
    const needle = search.toLowerCase()
    return run.name.toLowerCase().includes(needle) || run.pipeline.toLowerCase().includes(needle)
  }
  return true
}

export function RunsScreen() {
  const { data, isPending, error, refetch } = useRuns()
  const [filter, setFilter] = useState<'all' | 'running' | 'failed'>('all')
  const [search, setSearch] = useState('')

  useRunEvents()

  const runs = useMemo(
    () => (data?.runs ?? []).filter((run) => matches(run, filter, search)),
    [data, filter, search],
  )

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h1 className="text-2xl font-semibold text-zinc-900">Exécutions</h1>
        <p className="text-sm text-zinc-500">
          Toutes les exécutions du namespace, y compris celles déclenchées hors du dashboard.
        </p>
      </div>

      <div className="flex flex-wrap items-center gap-3">
        <div className="flex rounded-md border border-zinc-200 p-0.5" role="group" aria-label="Filtre">
          {FILTERS.map((entry) => (
            <button
              key={entry.value}
              type="button"
              aria-pressed={filter === entry.value}
              onClick={() => setFilter(entry.value)}
              className={`rounded px-3 py-1 text-sm font-medium transition-colors ${
                filter === entry.value ? 'bg-zinc-900 text-white' : 'text-zinc-600 hover:bg-zinc-100'
              }`}
            >
              {entry.label}
            </button>
          ))}
        </div>

        <input
          type="search"
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          placeholder="Filtrer par nom ou pipeline"
          aria-label="Filtrer par nom ou pipeline"
          className="min-w-64 flex-1 rounded-md border border-zinc-200 bg-white px-3 py-1.5 text-sm text-zinc-900 placeholder:text-zinc-400"
        />
      </div>

      {isPending && (
        <Card>
          {/* Squelettes aux dimensions des lignes attendues : un spinner ferait
              sauter la mise en page au moment où les données arrivent. */}
          <div className="flex flex-col gap-2">
            {Array.from({ length: 6 }, (_, index) => (
              <Skeleton key={index} className="h-10 w-full" />
            ))}
          </div>
        </Card>
      )}

      {error && <ErrorState message="Les exécutions n'ont pas pu être chargées." onRetry={() => void refetch()} />}

      {!isPending && !error && runs.length === 0 && (
        <EmptyState>
          {data?.runs.length ? 'Aucune exécution ne correspond au filtre.' : 'Aucune exécution observée pour le moment.'}
        </EmptyState>
      )}

      {runs.length > 0 && (
        <Card className="overflow-x-auto p-0">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-zinc-200 text-left text-xs text-zinc-500">
                <th className="px-4 py-2 font-medium">Exécution</th>
                <th className="px-4 py-2 font-medium">Pipeline</th>
                <th className="px-4 py-2 font-medium">Statut</th>
                <th className="px-4 py-2 font-medium">Début</th>
                <th className="px-4 py-2 font-medium">Durée</th>
              </tr>
            </thead>
            <tbody>
              {runs.map((run) => (
                <RunRow key={run.id} run={run} />
              ))}
            </tbody>
          </table>
        </Card>
      )}
    </div>
  )
}

function RunRow({ run }: { run: Run }) {
  return (
    <tr className="border-b border-zinc-100 last:border-0 hover:bg-zinc-50">
      <td className="px-4 py-2">
        <Link
          to={`/runs/${encodeURIComponent(run.name)}`}
          className="flex items-center gap-1.5 font-mono text-xs text-zinc-900 hover:underline"
        >
          {run.name}
          {run.observed && (
            // Un run observé est en lecture seule : le dashboard ne le relance
            // ni ne l'annule, puisqu'il ne l'a pas lancé.
            <span className="rounded border border-zinc-200 px-1 text-[10px] font-medium text-zinc-500">
              observée
            </span>
          )}
        </Link>
      </td>
      <td className="px-4 py-2 text-zinc-600">{run.pipeline}</td>
      <td className="px-4 py-2">
        <RunStatusCell status={run.status} reason={run.reason} />
      </td>
      <td className="px-4 py-2 text-zinc-600">{run.started_at ? formatDateTime(run.started_at) : '—'}</td>
      <td className="px-4 py-2 tabular-nums text-zinc-600">
        {formatDuration(duration(run.started_at, run.completed_at))}
      </td>
    </tr>
  )
}

function RunStatusCell({ status, reason }: { status: RunStatus; reason?: string }) {
  const style = runStatus(status)

  return (
    <span className="flex items-center gap-2">
      <StatusBadge style={style} />
      {/* Le motif Tekton est montré mais ne décide de rien : il change d'une
          version à l'autre, et l'interface s'appuie sur le statut. */}
      {reason && reason !== style.label && (
        <span className="hidden font-mono text-xs text-zinc-400 lg:inline">{reason}</span>
      )}
    </span>
  )
}

export function RunsScreenHeaderIcon() {
  return <Icon name="beaker" className="size-4" />
}
