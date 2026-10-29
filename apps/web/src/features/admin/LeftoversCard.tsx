import { useState } from 'react'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Card } from '../../components/primitives'
import { ApiError } from '../../lib/api'
import { formatDateTime } from '../../lib/format'
import { type Leftover, useLeftovers, usePurgeLeftover } from './installations'

/*
 * Ce que les plugins désinstallés ont laissé.
 *
 * Désinstaller garde les tables d'un plugin : le réinstaller retrouve ses
 * données. Les purger est un geste à part, irréversible, confirmé en retapant
 * l'identifiant du plugin. Seules ses tables partent : les faits de ses runs, les
 * jobs et l'audit sont ceux du cœur, et s'affichent sans lui.
 */
export function LeftoversCard() {
  const [purging, setPurging] = useState<string[]>([])
  const { data } = useLeftovers(purging.length > 0)
  const purge = usePurgeLeftover()
  const [asked, setAsked] = useState<Leftover | null>(null)

  const leftovers = data?.leftovers ?? []
  if (leftovers.length === 0) return null

  const failure =
    purge.error instanceof ApiError && purge.error.detail
      ? `${purge.error.message} : ${purge.error.detail}`
      : purge.error?.message

  return (
    <Card title="Données conservées">
      <p className="mb-3 text-sm text-zinc-500">
        Les tables des plugins désinstallés. Réinstallé, un plugin les retrouve ; purgées, elles sont
        perdues.
      </p>
      <ul className="flex flex-col gap-2">
        {leftovers.map((leftover) => (
          <li
            key={leftover.plugin}
            className="flex flex-wrap items-baseline gap-x-3 gap-y-1 rounded-md border border-zinc-200 px-3 py-2 text-sm"
          >
            <span className="font-medium text-zinc-900">
              {leftover.plugin} {leftover.version}
            </span>
            <span className="font-mono text-xs text-zinc-500">{leftover.table}</span>
            {leftover.removed_at && (
              <span className="text-xs text-zinc-400">
                désinstallé le {formatDateTime(leftover.removed_at)}
              </span>
            )}
            <span className="ml-auto">
              {purging.includes(leftover.plugin) ? (
                <span className="text-xs text-zinc-500">Purge en cours</span>
              ) : (
                <button
                  type="button"
                  onClick={() => setAsked(leftover)}
                  className="rounded-md border border-zinc-300 bg-white px-2.5 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
                >
                  Purger
                </button>
              )}
            </span>
          </li>
        ))}
      </ul>
      {failure && <p className="mt-3 text-sm text-critical-700">{failure}</p>}

      {asked && (
        <ConfirmDialog
          title={`Purger ${asked.plugin}`}
          confirmLabel="Purger"
          challenge={asked.plugin}
          pending={purge.isPending}
          onCancel={() => setAsked(null)}
          onConfirm={() =>
            purge.mutate(asked.plugin, {
              onSuccess: () => {
                setPurging((current) => [...current, asked.plugin])
                setAsked(null)
              },
            })
          }
        >
          <p>
            Les migrations de {asked.plugin} {asked.version} sont défaites, puis sa table de versions{' '}
            <span className="font-mono">{asked.table}</span> est supprimée.
          </p>
          <p>Ses tables et leurs lignes sont perdues ; les faits, les jobs et l'audit restent.</p>
        </ConfirmDialog>
      )}
    </Card>
  )
}
