import { type ReactNode, useState } from 'react'
import { useParams } from 'react-router'
import { Card, ErrorState, Skeleton } from '../../components/primitives'
import { ApiError } from '../../lib/api'
import { useSession } from '../auth/session'
import { browser, SCOPE_READ, SCOPE_RUN, useAgentRequest, useAnswerAgentRequest } from './api'

/*
 * Le consentement : un agent IA demande à agir au nom de la personne connectée.
 *
 * Le nom de l'agent est celui qu'il se donne ; l'hôte où le navigateur repart
 * dit d'où il vient. L'agent lit toujours ; agir est une case que la personne
 * peut décocher. Il agit avec les rôles de la personne, ni plus ni moins.
 */
export function ConsentScreen() {
  const { request: id = '' } = useParams()
  const { data, isLoading, error } = useAgentRequest(id)
  const answer = useAnswerAgentRequest(id)
  const session = useSession()
  const [run, setRun] = useState(true)

  if (isLoading) {
    return (
      <Centered>
        <Skeleton className="h-48 w-full" />
      </Centered>
    )
  }
  if (error || !data) {
    const expired = error instanceof ApiError && error.status === 404
    return (
      <Centered>
        <ErrorState
          message={
            expired
              ? 'Cette demande est inconnue ou expirée : relancez la connexion depuis l’agent.'
              : 'La demande ne peut pas être lue.'
          }
        />
      </Centered>
    )
  }

  const asksRun = data.scopes.includes(SCOPE_RUN)
  const username = session.kind === 'authenticated' ? session.identity.username : ''
  const send = (approve: boolean) =>
    answer.mutate(
      approve ? { approve: true, scopes: run && asksRun ? [SCOPE_READ, SCOPE_RUN] : [SCOPE_READ] } : { approve: false },
      {
        onSuccess: ({ redirect }) => browser.leaveTo(redirect),
      },
    )

  return (
    <Centered>
      <Card title="Autoriser un agent IA">
        <p className="text-sm text-zinc-700">
          <span className="font-medium text-zinc-900">{data.client}</span> demande à agir sur le dashboard au nom de{' '}
          <span className="font-medium text-zinc-900">{username}</span>, avec vos rôles.
        </p>
        <p className="mt-1 text-xs text-zinc-500">
          Le navigateur repartira vers <span className="font-mono">{data.redirect_host}</span>.
        </p>

        <ul className="mt-4 flex flex-col gap-2 text-sm">
          <li className="rounded-md border border-zinc-200 px-3 py-2 text-zinc-700">
            Consulter les projets, les exécutions et leurs journaux, la file d'attente, les releases et leurs résultats,
            les pull requests et les statistiques
          </li>
          {asksRun && (
            <li className="rounded-md border border-zinc-200 px-3 py-2">
              <label className="flex items-start gap-2 text-zinc-700">
                <input
                  type="checkbox"
                  checked={run}
                  onChange={(event) => setRun(event.target.checked)}
                  className="mt-0.5"
                />
                Lancer et annuler des pipelines, créer, construire, lancer et approuver des releases
              </label>
            </li>
          )}
        </ul>
        <p className="mt-3 text-xs text-zinc-500">Vous pourrez révoquer cet agent dans Administration, Agents IA.</p>

        {answer.error && <p className="mt-3 text-sm text-critical-700">{answer.error.message}</p>}
        <div className="mt-4 flex justify-end gap-2">
          <button
            type="button"
            disabled={answer.isPending}
            onClick={() => send(false)}
            className="rounded-md border border-zinc-300 bg-white px-3 py-2 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-50"
          >
            Refuser
          </button>
          <button
            type="button"
            disabled={answer.isPending}
            onClick={() => send(true)}
            className="rounded-md bg-zinc-900 px-3 py-2 text-sm font-medium text-white transition-colors hover:bg-zinc-700 disabled:opacity-50"
          >
            Autoriser
          </button>
        </div>
      </Card>
    </Centered>
  )
}

function Centered({ children }: { children: ReactNode }) {
  return <div className="mx-auto max-w-lg py-8">{children}</div>
}
