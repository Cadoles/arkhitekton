import { useState } from 'react'
import { Link } from 'react-router'
import { Icon } from '../../components/Icon'
import { Card, EmptyState, ErrorState, Skeleton } from '../../components/primitives'
import { formatAge, formatDateTime } from '../../lib/format'
import { hasAtLeast, useSession } from '../auth/session'
import { explainWait, useCancel, useQueue, type Job, type Queue } from './api'

/*
 * La file d'attente : ce qui tourne, ce qui attend, et pourquoi.
 *
 * L'écran répond à la question d'un opérateur devant une file immobile :
 * **pourquoi ça n'avance pas ?** Une seule liste, dans l'ordre du service, où
 * chaque job dit sur quoi il porte (projet, release ou pull request) et ce qui le
 * retient. Les files ne sont plus qu'une ligne d'occupation au-dessus : une carte
 * par type empilait des files presque toutes vides, et on y cherchait les jobs.
 */
export function QueueScreen() {
  const { data, isPending, error, refetch } = useQueue()
  const session = useSession()

  const canCancel = session.kind === 'authenticated' && hasAtLeast(session.identity.role, 'operator')
  const isAdmin = session.kind === 'authenticated' && hasAtLeast(session.identity.role, 'admin')

  const queues = data?.queues ?? []
  const jobs = data?.jobs ?? []
  const running = jobs.filter((job) => job.state === 'running')
  const waiting = jobs.filter((job) => job.state === 'queued')
  const byKind = new Map(queues.map((queue) => [queue.kind, queue]))

  return (
    <Card
      title="File d'attente"
      action={
        isAdmin && (
          <Link to="/admin/pipelines" className="text-xs text-zinc-500 hover:underline">
            Régler les plafonds
          </Link>
        )
      }
    >
      {isPending && <Skeleton className="h-40 w-full" />}
      {error && <ErrorState message="La file n'a pas pu être chargée." onRetry={() => void refetch()} />}

      {data && (
        <div className="flex flex-col gap-4">
          <Occupancy queues={queues} running={running.length} waiting={waiting.length} max={data.max_concurrent} />

          {jobs.length === 0 ? (
            <EmptyState>Rien ne tourne ni n&apos;attend.</EmptyState>
          ) : (
            <>
              <JobList title="En cours" jobs={running} byKind={byKind} canCancel={canCancel} />
              <JobList title="En attente" jobs={waiting} byKind={byKind} canCancel={canCancel} />
            </>
          )}
        </div>
      )}
    </Card>
  )
}

/** L'occupation globale, et celle des files qui ont quelque chose ; les vides se replient. */
function Occupancy({
  queues,
  running,
  waiting,
  max,
}: {
  queues: Queue[]
  running: number
  waiting: number
  max: number
}) {
  const [showIdle, setShowIdle] = useState(false)
  const busy = queues.filter((queue) => queue.running + queue.waiting > 0)
  const idle = queues.filter((queue) => queue.running + queue.waiting === 0)

  return (
    <div className="flex flex-col gap-2 text-xs">
      <p className="text-sm text-zinc-600">
        <span className="tabular-nums">{running}</span> en cours sur <span className="tabular-nums">{max}</span> au
        total · <span className="tabular-nums">{waiting}</span> en attente
      </p>
      <div className="flex flex-wrap items-center gap-1.5">
        {busy.map((queue) => (
          <QueueChip key={queue.kind} queue={queue} />
        ))}
        {idle.length > 0 && (
          <button
            type="button"
            aria-expanded={showIdle}
            onClick={() => setShowIdle((shown) => !shown)}
            className="inline-flex items-center gap-1 text-zinc-500 hover:underline"
          >
            {busy.length > 0 ? `${idle.length} autres files vides` : `${idle.length} files, toutes vides`}
            <Icon name="chevron" className={`size-3 transition-transform ${showIdle ? '' : '-rotate-90'}`} />
          </button>
        )}
      </div>
      {showIdle && (
        <div className="flex flex-wrap gap-1.5">
          {idle.map((queue) => (
            <QueueChip key={queue.kind} queue={queue} />
          ))}
        </div>
      )}
    </div>
  )
}

function QueueChip({ queue }: { queue: Queue }) {
  const full = queue.running >= queue.max_concurrent
  const tone = full
    ? 'border-warn-500 bg-warn-50 text-warn-700'
    : queue.running + queue.waiting > 0
      ? 'border-zinc-400 text-zinc-800'
      : 'border-zinc-200 text-zinc-500'
  // L'occupation avant le plafond : c'est leur rapport qui dit s'il reste de la place.
  return (
    <span
      className={`inline-flex items-center gap-1 rounded border px-1.5 py-0.5 ${tone}`}
      title={queue.overridden ? `Plafond réglé, ${queue.default_max_concurrent} par défaut` : undefined}
    >
      {queue.label}
      <span className="tabular-nums">
        {queue.running}/{queue.max_concurrent}
      </span>
      {full && <span>pleine</span>}
      {queue.waiting > 0 && <span className="tabular-nums">· {queue.waiting} en attente</span>}
      {/* Une tâche locale ne devient pas un PipelineRun, mais occupe bien une file. */}
      {!queue.known && <span className="text-zinc-400">interne</span>}
    </span>
  )
}

function JobList({
  title,
  jobs,
  byKind,
  canCancel,
}: {
  title: string
  jobs: Job[]
  byKind: Map<string, Queue>
  canCancel: boolean
}) {
  if (jobs.length === 0) return null
  // Le rang dans sa file : un job retenu par sa file ne retient pas ceux des autres, et un
  // rang global dirait qu'il attend derrière eux.
  const seen = new Map<string, number>()
  return (
    <section className="flex flex-col gap-1.5">
      <h3 className="text-xs font-medium tracking-wide text-zinc-500 uppercase">
        {title} <span className="tabular-nums">({jobs.length})</span>
      </h3>
      <ul className="flex flex-col divide-y divide-zinc-200 rounded-md border border-zinc-200">
        {jobs.map((job) => {
          const rank = (seen.get(job.kind) ?? 0) + 1
          seen.set(job.kind, rank)
          return (
            <JobRow
              key={job.id}
              job={job}
              queue={byKind.get(job.kind)}
              rank={job.state === 'queued' ? rank : undefined}
              canCancel={canCancel}
            />
          )
        })}
      </ul>
    </section>
  )
}

function JobRow({ job, queue, rank, canCancel }: { job: Job; queue?: Queue; rank?: number; canCancel: boolean }) {
  const cancel = useCancel()
  const subject = job.release_tag ?? (job.pull_request ? `PR #${job.pull_request}` : undefined)

  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2 text-sm">
      {job.state === 'running' ? (
        <span className="size-2 shrink-0 animate-pulse rounded-full bg-healthy-500" aria-hidden />
      ) : (
        <span className="w-5 shrink-0 text-right text-xs text-zinc-400 tabular-nums" title="Rang dans sa file">
          {rank}
        </span>
      )}

      <span className="font-medium text-zinc-900">{job.label}</span>
      {job.project && <span className="text-zinc-600">{job.project}</span>}
      {subject && <span className="font-mono text-xs text-zinc-700">{subject}</span>}

      {job.attempt > 1 && (
        // Un job qui a déjà échoué à démarrer : le signe d'un problème côté cluster.
        <span className="rounded border border-warn-500 bg-warn-50 px-1.5 py-0.5 text-xs text-warn-700">
          essai {job.attempt}
        </span>
      )}

      {job.state === 'running' ? (
        <span className="text-xs text-zinc-500">
          {job.started_at && <>démarré {formatAge(job.started_at)}</>}
          {job.run_name && (
            <>
              {' · '}
              <Link to={`/runs/${encodeURIComponent(job.run_name)}`} className="font-mono hover:underline">
                {job.run_name}
              </Link>
            </>
          )}
        </span>
      ) : (
        <span className="flex items-center gap-1 text-xs text-zinc-500">
          <Icon name="clock" className="size-3.5" />
          {explainWait(job, queue)}
        </span>
      )}

      <span className="ml-auto text-xs text-zinc-400" title={formatDateTime(job.requested_at)}>
        {job.requested_by} · {formatAge(job.requested_at)}
      </span>

      {canCancel && (
        <button
          type="button"
          disabled={cancel.isPending}
          onClick={() => cancel.mutate(job.id)}
          className="rounded-md border border-zinc-200 px-2 py-0.5 text-xs font-medium text-zinc-600 transition-colors hover:bg-zinc-100 disabled:opacity-50"
        >
          Annuler
        </button>
      )}
    </li>
  )
}
