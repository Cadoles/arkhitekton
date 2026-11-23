import { useState } from 'react'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Icon } from '../../components/Icon'
import { Card, EmptyState, ErrorState, Skeleton } from '../../components/primitives'
import { copyText } from '../../lib/clipboard'
import { formatDateTime } from '../../lib/format'
import { type Agent, SCOPE_RUN, useAgents, useRevokeAgent } from './api'
import { guides } from './guides'

/*
 * Les agents IA autorisés : comment en brancher un, et la révocation.
 *
 * Chacun gère les siens ; un administrateur voit ceux de tout le monde, avec
 * leur propriétaire, et peut les révoquer.
 */
export function AgentsScreen() {
  const { data, isLoading, error, refetch } = useAgents()
  const revoke = useRevokeAgent()
  const [asked, setAsked] = useState<Agent | null>(null)

  if (isLoading) return <Skeleton className="h-40 w-full" />
  if (error || !data) return <ErrorState message="Les agents ne peuvent pas être lus." onRetry={() => void refetch()} />

  return (
    <div className="flex flex-col gap-4">
      <Card title="Brancher un agent">
        <p className="mb-3 text-sm text-zinc-600">
          Un agent IA compatible MCP consulte et pilote le dashboard à cette adresse. À sa première connexion, il ouvre
          le dashboard dans le navigateur, où vous l'autorisez ; il agit ensuite avec vos rôles.
        </p>
        <CopyLine label="Adresse du serveur MCP" value={data.mcp_url} />
        <Guides url={data.mcp_url} />
      </Card>

      <Card title={data.all ? 'Agents autorisés' : 'Vos agents autorisés'}>
        {data.agents.length === 0 ? (
          <EmptyState>Aucun agent autorisé.</EmptyState>
        ) : (
          <ul className="flex flex-col gap-2">
            {data.agents.map((agent) => (
              <li
                key={agent.id}
                className="flex flex-wrap items-baseline gap-x-3 gap-y-1 rounded-md border border-zinc-200 px-3 py-2 text-sm"
              >
                <span className="font-medium text-zinc-900">{agent.name}</span>
                {agent.owner && <span className="text-zinc-500">de {agent.owner}</span>}
                <span className="rounded border border-zinc-200 px-1.5 py-0.5 text-xs text-zinc-600">
                  {agent.scopes.includes(SCOPE_RUN) ? 'consulte et agit' : 'consulte'}
                </span>
                <span className="text-xs text-zinc-400">autorisé le {formatDateTime(agent.created_at)}</span>
                {agent.last_used && (
                  <span className="text-xs text-zinc-400">utilisé le {formatDateTime(agent.last_used)}</span>
                )}
                <span className="ml-auto">
                  <button
                    type="button"
                    onClick={() => setAsked(agent)}
                    className="rounded-md border border-zinc-300 bg-white px-2.5 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
                  >
                    Révoquer
                  </button>
                </span>
              </li>
            ))}
          </ul>
        )}
        {revoke.error && <p className="mt-3 text-sm text-critical-700">{revoke.error.message}</p>}
      </Card>

      {asked && (
        <ConfirmDialog
          title={`Révoquer ${asked.name}`}
          confirmLabel="Révoquer"
          pending={revoke.isPending}
          onCancel={() => setAsked(null)}
          onConfirm={() => revoke.mutate(asked.id, { onSuccess: () => setAsked(null) })}
        >
          <p>Ses jetons cessent de fonctionner aussitôt. Pour s'en servir à nouveau, il faudra l'autoriser.</p>
        </ConfirmDialog>
      )}
    </div>
  )
}

/** La marche à suivre pour un agent, choisi parmi les plus courants. */
function Guides({ url }: { url: string }) {
  const all = guides(url)
  const [chosen, setChosen] = useState('claude-code')
  const guide = all.find((candidate) => candidate.id === chosen)
  if (!guide) return null
  return (
    <div className="mt-4">
      <div role="group" aria-label="Agent" className="flex flex-wrap gap-1.5">
        {all.map((candidate) => (
          <button
            key={candidate.id}
            type="button"
            aria-pressed={candidate.id === guide.id}
            onClick={() => setChosen(candidate.id)}
            className={`rounded-md border px-2.5 py-1 text-xs font-medium transition-colors ${
              candidate.id === guide.id
                ? 'border-zinc-900 bg-zinc-900 text-white'
                : 'border-zinc-300 bg-white text-zinc-700 hover:bg-zinc-100'
            }`}
          >
            {candidate.label}
          </button>
        ))}
      </div>
      <ol className="mt-3 flex list-decimal flex-col gap-2 pl-5 text-sm text-zinc-700">
        {guide.steps.map((step) => (
          <li key={step.text}>
            {step.text}
            {step.code && <CopyBlock value={step.code} />}
          </li>
        ))}
      </ol>
      {guide.note && <p className="mt-2 text-xs text-zinc-500">{guide.note}</p>}
      <p className="mt-3 text-xs text-zinc-500">
        Tout autre client MCP se branche de même : transport HTTP « streamable », l'adresse ci-dessus ; il découvre seul
        l'autorisation.
      </p>
    </div>
  )
}

function CopyLine({ label, value }: { label: string; value: string }) {
  return (
    <div className="mt-2">
      <div className="text-xs text-zinc-500">{label}</div>
      <CopyBlock value={value} />
    </div>
  )
}

function CopyBlock({ value }: { value: string }) {
  const [state, setState] = useState<'idle' | 'copied' | 'failed'>('idle')
  return (
    <div className="mt-1 flex items-start gap-2">
      <pre className="min-w-0 flex-1 overflow-x-auto rounded-md bg-zinc-100 px-2 py-1.5 font-mono text-xs text-zinc-800">
        {value}
      </pre>
      <button
        type="button"
        onClick={() => void copyText(value).then((done) => setState(done ? 'copied' : 'failed'))}
        className="inline-flex shrink-0 items-center gap-1.5 rounded-md border border-zinc-300 bg-white px-2.5 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
      >
        <Icon name={state === 'copied' ? 'check' : 'link'} className="size-3.5" />
        {state === 'copied' ? 'Copiée' : state === 'failed' ? 'Copie refusée' : 'Copier'}
      </button>
    </div>
  )
}
