import { Link, useParams } from 'react-router'
import { Card, EmptyState, ErrorState, Skeleton } from '../../components/primitives'
import { formatDateTime } from '../../lib/format'
import { useEnvironmentHistory, useEnvironments } from './api'

/*
 * Historique des promotions d'un environnement.
 *
 * La source est ce que le dashboard a **demandé**, et non les commits que Flux
 * écrit dans `mse-kustom`. Les deux diffèrent : une promotion qu'aucune
 * ImagePolicy ne retient n'apparaît nulle part côté Git alors qu'elle a bien eu
 * lieu — et c'est précisément ce cas qu'il faut pouvoir constater.
 */
export function HistoryScreen() {
  const { env } = useParams()
  const { data, isPending, error, refetch } = useEnvironmentHistory(env)
  const environments = useEnvironments()

  const environment = environments.data?.environments.find((entry) => entry.id === env)

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-baseline gap-3">
        <h1 className="text-2xl font-semibold text-zinc-900">{environment?.label ?? env}</h1>
        <Link to="/environments" className="text-sm text-zinc-500 hover:underline">
          Tous les environnements
        </Link>
      </div>

      {isPending && <Skeleton className="h-48 w-full" />}
      {error && <ErrorState message="L'historique n'a pas pu être chargé." onRetry={() => void refetch()} />}

      {data && (
        <Card title="Promotions demandées">
          {data.history.length === 0 ? (
            <EmptyState>Aucun historique pour cet environnement.</EmptyState>
          ) : (
            <ul className="flex flex-col gap-2">
              {data.history.map((entry) => (
                <li
                  key={`${entry.tag}-${entry.pushed_at}`}
                  className="flex flex-wrap items-baseline gap-x-3 gap-y-1 rounded-md border border-zinc-200 px-3 py-2 text-sm"
                >
                  <Link
                    to={`/releases/${encodeURIComponent(entry.release)}`}
                    className="font-mono font-medium text-zinc-900 hover:underline"
                  >
                    {entry.release}
                  </Link>
                  <span className="font-mono text-zinc-500">{entry.tag}</span>

                  {/* Rappelée seulement quand elle diffère du tag de la release,
                      c'est-à-dire sur une finale produite par re-tag. */}
                  {entry.image_version !== entry.release && (
                    <span className="text-zinc-500">
                      image <span className="font-mono">{entry.image_version}</span>
                    </span>
                  )}

                  {entry.job_state && <span className="text-zinc-500">{entry.job_state}</span>}
                  <span className="ml-auto text-zinc-500">
                    {entry.requested_by && `${entry.requested_by} — `}
                    {formatDateTime(entry.pushed_at)}
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
