import { useState } from 'react'
import { Card, EmptyState, Skeleton } from '../../components/primitives'
import { formatDateTime } from '../../lib/format'
import { ACTION_LABEL, useAudit } from './api'

/*
 * Journal d'audit : qui a fait quoi, et quand.
 *
 * Il répond à une question qu'on se pose **après coup** — « qui a promu en
 * production », « qui a retiré ce rôle » — ce qui justifie son propre onglet :
 * empilé sous les rôles, il obligeait à faire défiler une page entière pour
 * atteindre ce qu'on venait chercher.
 */
export function AuditScreen() {
  return <AuditCard />
}

function AuditCard() {
  const [target, setTarget] = useState('')
  const [query, setQuery] = useState('')
  const { data, isPending } = useAudit(target)

  return (
    <Card
      title="Journal d'audit"
      action={
        <form
          className="flex gap-2"
          onSubmit={(event) => {
            event.preventDefault()
            setTarget(query.trim())
          }}
        >
          <input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="filtrer par cible…"
            aria-label="Filtrer le journal par cible"
            className="rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-sm text-zinc-900"
          />
          <button
            type="submit"
            className="rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
          >
            Filtrer
          </button>
        </form>
      }
    >
      {isPending && <Skeleton className="h-32 w-full" />}

      {data &&
        (data.entries.length === 0 ? (
          <EmptyState>{target ? `Aucune action sur « ${target} ».` : 'Aucune action consignée.'}</EmptyState>
        ) : (
          <ul className="flex flex-col">
            {data.entries.map((entry) => (
              <li
                key={entry.id}
                className="flex flex-wrap items-baseline gap-x-3 gap-y-1 border-t border-zinc-200 py-1.5 text-sm first:border-t-0"
              >
                <span className="min-w-44 font-medium text-zinc-900">
                  {ACTION_LABEL[entry.action] ?? entry.action}
                </span>
                {entry.target && <span className="font-mono text-zinc-600">{entry.target}</span>}
                <span className="text-zinc-500">{entry.actor}</span>
                {entry.payload && (
                  <span className="text-xs text-zinc-500">
                    {Object.entries(entry.payload)
                      .map(([key, value]) => `${key}=${String(value)}`)
                      .join(' ')}
                  </span>
                )}
                <span className="ml-auto text-zinc-500">{formatDateTime(entry.at)}</span>
              </li>
            ))}
          </ul>
        ))}
    </Card>
  )
}
