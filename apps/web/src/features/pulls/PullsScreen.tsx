import { useState } from 'react'
import { Link } from 'react-router'
import { Icon } from '../../components/Icon'
import { Card, EmptyState, ErrorState, Skeleton, StatusBadge } from '../../components/primitives'
import { formatDateTime } from '../../lib/format'
import { status } from '../../lib/status'
import { usePulls, type PullFilter } from './api'

/*
 * Les pull requests du projet, telles que le worker les suit sur la forge.
 *
 * Ouvertes par défaut : c'est ce sur quoi l'on agit. Les fermées et fusionnées restent
 * consultables, avec ce qui a tourné dessus.
 */

const FILTERS: { value: PullFilter; label: string }[] = [
  { value: 'open', label: 'Ouvertes' },
  { value: 'closed', label: 'Fermées' },
  { value: 'all', label: 'Toutes' },
]

export function PullsScreen() {
  const [filter, setFilter] = useState<PullFilter>('open')
  const { data, isPending, error, refetch } = usePulls(filter)

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-2xl font-semibold text-zinc-900">Pull requests</h1>
        <div className="flex gap-1" role="group" aria-label="Filtrer les pull requests">
          {FILTERS.map((entry) => (
            <button
              key={entry.value}
              type="button"
              onClick={() => setFilter(entry.value)}
              aria-pressed={filter === entry.value}
              className={`rounded-md border px-3 py-1 text-sm font-medium transition-colors ${
                filter === entry.value
                  ? 'border-zinc-900 bg-zinc-900 text-white'
                  : 'border-zinc-300 bg-white text-zinc-600 hover:bg-zinc-100'
              }`}
            >
              {entry.label}
            </button>
          ))}
        </div>
      </div>

      {isPending && <Skeleton className="h-48 w-full" />}
      {error && <ErrorState message="Les pull requests n'ont pas pu être chargées." onRetry={() => void refetch()} />}

      {data && (
        <Card>
          {data.pulls.length === 0 ? (
            <EmptyState>
              Aucune pull request {filter === 'open' ? 'ouverte' : filter === 'closed' ? 'fermée' : ''}. Elles
              sont lues sur la forge du projet, à la minute ou dès que son webhook en livre une.
            </EmptyState>
          ) : (
            <ul className="flex flex-col gap-2">
              {data.pulls.map((pull) => (
                <li
                  key={pull.number}
                  className="flex flex-wrap items-baseline gap-x-3 gap-y-1 rounded-md border border-zinc-200 px-3 py-2"
                >
                  <Link to={`/pulls/${pull.number}`} className="font-medium text-zinc-900 hover:underline">
                    <span className="font-mono text-zinc-500">#{pull.number}</span> {pull.title}
                  </Link>
                  <StatusBadge style={status.pull(pull.state)} />
                  <span className="flex items-center gap-1 text-sm text-zinc-500">
                    <Icon name="branch" className="size-3.5" />
                    <span className="font-mono">{pull.source_branch}</span> → <span className="font-mono">{pull.target_branch}</span>
                  </span>
                  <span className="ml-auto text-sm text-zinc-500">
                    {pull.author}
                    {pull.updated_at && <> — {formatDateTime(pull.updated_at)}</>}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </Card>
      )}
    </div>
  )
}
