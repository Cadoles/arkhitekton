import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { Icon } from '../../components/Icon'
import { Card, EmptyState, ErrorState, Skeleton } from '../../components/primitives'
import { formatDateTime } from '../../lib/format'
import { hasAtLeast, useSession } from '../auth/session'
import { explainWait, useCancel, useLaunch, useQueue, type Job, type Kind } from './api'

/*
 * File d'exécution.
 *
 * L'écran répond à une seule question, celle que pose un opérateur devant une
 * file immobile : **pourquoi ça n'avance pas ?** Tout le reste — priorité, essais,
 * classe — n'est là que pour étayer la réponse.
 */
export function QueueScreen() {
  const { data, isPending, error, refetch } = useQueue()
  const session = useSession()

  const canLaunch = session.kind === 'authenticated' && hasAtLeast(session.identity.role, 'operator')

  const running = useMemo(() => (data?.jobs ?? []).filter((job) => job.state === 'running'), [data])
  const waiting = useMemo(() => (data?.jobs ?? []).filter((job) => job.state === 'queued'), [data])

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h1 className="text-2xl font-semibold text-zinc-900">File d'attente</h1>
        {data && (
          <p className="text-sm text-zinc-500">
            {running.length} en cours sur {data.max_concurrent} — {waiting.length} en attente
          </p>
        )}
      </div>

      {isPending && <Skeleton className="h-40 w-full" />}
      {error && <ErrorState message="La file n'a pas pu être chargée." onRetry={() => void refetch()} />}

      {data && (
        <>
          {canLaunch && <LaunchPanel kinds={data.kinds} />}

          <Card title={`En cours (${running.length})`}>
            {running.length === 0 ? (
              <EmptyState>Aucune exécution en cours.</EmptyState>
            ) : (
              <ul className="flex flex-col gap-2">
                {running.map((job) => (
                  <JobRow key={job.id} job={job} kinds={data.kinds} canCancel={canLaunch} />
                ))}
              </ul>
            )}
          </Card>

          <Card title={`En attente (${waiting.length})`}>
            {waiting.length === 0 ? (
              <EmptyState>Rien n'attend.</EmptyState>
            ) : (
              <ul className="flex flex-col gap-2">
                {/* L'ordre est celui du service, pas un autre : une file affichée
                    dans un ordre différent de celui où elle sera servie fait
                    attendre le mauvais job. */}
                {waiting.map((job, index) => (
                  <JobRow key={job.id} job={job} kinds={data.kinds} canCancel={canLaunch} rank={index + 1} />
                ))}
              </ul>
            )}
          </Card>
        </>
      )}
    </div>
  )
}

function JobRow({
  job,
  kinds,
  canCancel,
  rank,
}: {
  job: Job
  kinds: Kind[]
  canCancel: boolean
  rank?: number
}) {
  const cancel = useCancel()

  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border border-zinc-200 px-3 py-2">
      {rank !== undefined && (
        <span className="w-6 shrink-0 text-right text-xs tabular-nums text-zinc-400">{rank}</span>
      )}

      <span className="font-medium text-zinc-900">{job.label}</span>

      <span className="rounded border border-zinc-200 px-1.5 py-0.5 font-mono text-xs text-zinc-500">
        {job.queue_class}
      </span>

      {job.attempt > 1 && (
        // Un job qui a déjà échoué à démarrer mérite d'être signalé : c'est le
        // signe d'un problème côté cluster, pas d'une attente ordinaire.
        <span className="rounded border border-warn-500 bg-warn-50 px-1.5 py-0.5 text-xs text-warn-700">
          essai {job.attempt}
        </span>
      )}

      {job.state === 'running' && job.run_name && (
        <Link to={`/runs/${encodeURIComponent(job.run_name)}`} className="font-mono text-xs text-zinc-600 hover:underline">
          {job.run_name}
        </Link>
      )}

      {job.state === 'queued' && (
        <span className="flex items-center gap-1.5 text-xs text-zinc-500">
          <Icon name="clock" className="size-3.5" />
          {explainWait(job, kinds)}
        </span>
      )}

      <span className="ml-auto text-xs text-zinc-400">
        {job.requested_by} · {formatDateTime(job.requested_at)}
      </span>

      {canCancel && (
        <button
          type="button"
          disabled={cancel.isPending}
          onClick={() => cancel.mutate(job.id)}
          className="rounded-md border border-zinc-200 px-2 py-1 text-xs font-medium text-zinc-600 transition-colors hover:bg-zinc-100 disabled:opacity-50"
        >
          Annuler
        </button>
      )}
    </li>
  )
}

function LaunchPanel({ kinds }: { kinds: Kind[] }) {
  const [selected, setSelected] = useState(kinds[0]?.kind ?? '')
  const [params, setParams] = useState<Record<string, string>>({})
  const launch = useLaunch()

  const kind = kinds.find((entry) => entry.kind === selected)
  const inputs = kind?.required_inputs ?? []
  const complete = inputs.every((name) => (params[name] ?? '').trim() !== '')

  return (
    <Card title="Lancer une exécution">
      <div className="flex flex-wrap items-end gap-3">
        <label className="flex flex-col gap-1">
          <span className="text-xs text-zinc-500">Type</span>
          <select
            value={selected}
            onChange={(event) => {
              setSelected(event.target.value)
              // Les paramètres sont repartis de zéro : garder ceux du type
              // précédent enverrait des valeurs qui ne veulent rien dire ici.
              setParams({})
            }}
            className="rounded-md border border-zinc-200 bg-white px-2 py-1.5 text-sm text-zinc-900"
          >
            {kinds.map((entry) => (
              <option key={entry.kind} value={entry.kind}>
                {entry.label}
              </option>
            ))}
          </select>
        </label>

        {inputs.map((name) => (
          <label key={name} className="flex flex-col gap-1">
            <span className="text-xs text-zinc-500">{name}</span>
            <input
              value={params[name] ?? ''}
              onChange={(event) => setParams((previous) => ({ ...previous, [name]: event.target.value }))}
              className="rounded-md border border-zinc-200 bg-white px-2 py-1.5 text-sm text-zinc-900"
            />
          </label>
        ))}

        <button
          type="button"
          disabled={!complete || launch.isPending}
          onClick={() => launch.mutate({ kind: selected, params })}
          className="rounded-md bg-zinc-900 px-3 py-2 text-sm font-medium text-white transition-colors hover:bg-zinc-700 disabled:opacity-40"
        >
          Mettre en file
        </button>
      </div>

      {kind && (
        <p className="mt-3 text-xs text-zinc-500">
          Classe d'exclusion&nbsp;: <span className="font-mono">{kind.queue_class}</span>
          {kind.queue_class === 'exclusive-cluster' &&
            ' — cette exécution attendra que le cluster soit vide, et tournera seule.'}
        </p>
      )}

      {launch.isError && (
        <p className="mt-3 text-xs text-critical-700">
          Le lancement a été refusé. Vérifiez les paramètres.
        </p>
      )}
    </Card>
  )
}
