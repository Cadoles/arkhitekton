import { Link } from 'react-router'
import { Card, EmptyState, ErrorState, Skeleton, StatusBadge } from '../../components/primitives'
import { formatDateTime } from '../../lib/format'
import { status } from '../../lib/status'
import { ReleaseBadges } from './ReleaseBadges'
import { useReleases, type Release } from './api'

const KIND_LABEL: Record<Release['kind'], string> = {
  pre: 'Pré-release',
  final: 'Release',
  patch: 'Correctif',
}

/** La liste des releases, sous le titre que l'écran `releases` pose ; ses
 *  lignes ont un emplacement `badges`. */
export function ReleasesList() {
  const { data, isPending, error, refetch } = useReleases()

  return (
    <>
      {isPending && <Skeleton className="h-48 w-full" />}
      {error && <ErrorState message="Les releases n'ont pas pu être chargées." onRetry={() => void refetch()} />}

      {data && (
        <Card>
          {data.releases.length === 0 ? (
            <EmptyState>Aucune release enregistrée.</EmptyState>
          ) : (
            <ul className="flex flex-col gap-2">
              {data.releases.map((release) => (
                <li
                  key={release.tag}
                  className="flex flex-wrap items-baseline gap-x-3 gap-y-1 rounded-md border border-zinc-200 px-3 py-2"
                >
                  <Link to={`/releases/${encodeURIComponent(release.tag)}`} className="font-mono font-medium text-zinc-900 hover:underline">
                    {release.tag}
                  </Link>
                  <span className="text-sm text-zinc-500">{KIND_LABEL[release.kind]}</span>
                  {/* L'état en badge plutôt qu'en texte gris : c'est le seul
                      champ de la ligne qui porte un verdict, et il se lisait
                      comme une note de plus. */}
                  <StatusBadge style={status.release(release.state)} />
                  <ReleaseBadges release={release} />

                  {/* La version d'image n'est rappelée que lorsqu'elle diffère du
                      tag, c'est-à-dire sur une finale produite par re-tag. La
                      montrer partout noierait le seul cas où elle surprend. */}
                  {release.image_version !== release.tag && (
                    <span className="text-sm text-zinc-500">
                      image <span className="font-mono">{release.image_version}</span>
                    </span>
                  )}

                  <span className="ml-auto text-sm text-zinc-500">
                    {release.created_by} — {formatDateTime(release.created_at)}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </Card>
      )}
    </>
  )
}
