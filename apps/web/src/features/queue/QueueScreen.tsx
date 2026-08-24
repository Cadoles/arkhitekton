import { Link } from 'react-router'
import { Icon } from '../../components/Icon'
import { Card, EmptyState, ErrorState, Skeleton } from '../../components/primitives'
import { formatDateTime } from '../../lib/format'
import { hasAtLeast, useSession } from '../auth/session'
import { explainWait, useCancel, useQueue, type Job, type Queue } from './api'

/*
 * Les files d'attente, une par type d'exécution.
 *
 * L'écran répond à une seule question, celle que pose un opérateur devant une
 * file immobile : **pourquoi ça n'avance pas ?** Le regroupement par type est ce
 * qui la rend lisible — le modèle précédent mêlait tous les jobs dans une liste
 * unique, et il fallait savoir quels types partageaient une classe d'exclusion
 * pour comprendre qui retenait qui.
 *
 * Les files vides sont affichées aussi. Ne montrer que les occupées ferait
 * disparaître la moitié du tableau au repos, et c'est justement quand rien ne
 * tourne qu'on vient vérifier ce qu'une file autorise.
 */
export function QueueScreen() {
  const { data, isPending, error, refetch } = useQueue()
  const session = useSession()

  const canLaunch = session.kind === 'authenticated' && hasAtLeast(session.identity.role, 'operator')

  const running = (data?.queues ?? []).reduce((total, queue) => total + queue.running, 0)
  const waiting = (data?.queues ?? []).reduce((total, queue) => total + queue.waiting, 0)

  // Les files occupées passent devant : au repos, l'ordre du catalogue suffit,
  // mais dès qu'il se passe quelque chose c'est ce qui se passe qu'on cherche.
  const queues = [...(data?.queues ?? [])].sort(
    (a, b) => b.running + b.waiting - (a.running + a.waiting),
  )

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-lg font-semibold text-zinc-900">Files d'attente</h2>
        {data && (
          <p className="text-sm text-zinc-500">
            {running} en cours sur {data.max_concurrent} au total — {waiting} en attente
          </p>
        )}
      </div>

      {isPending && <Skeleton className="h-40 w-full" />}
      {error && <ErrorState message="La file n'a pas pu être chargée." onRetry={() => void refetch()} />}

      {data &&
        queues.map((queue) => (
          <QueueCard key={queue.kind} queue={queue} canCancel={canLaunch} />
        ))}
    </div>
  )
}

function QueueCard({ queue, canCancel }: { queue: Queue; canCancel: boolean }) {
  const full = queue.running >= queue.max_concurrent

  return (
    <Card>
      <div className="mb-3 flex flex-wrap items-center gap-x-3 gap-y-1">
        <h3 className="font-medium text-zinc-900">{queue.label}</h3>

        {/* L'occupation avant le plafond : c'est le rapport des deux qui dit s'il
            reste de la place, et le lire dans cet ordre évite de soustraire. */}
        <span
          className={`rounded border px-1.5 py-0.5 text-xs tabular-nums ${
            full
              ? 'border-warn-500 bg-warn-50 text-warn-700'
              : 'border-zinc-200 text-zinc-500'
          }`}
        >
          {queue.running} / {queue.max_concurrent} en parallèle
        </span>

        {queue.overridden && (
          <span className="rounded border border-zinc-200 px-1.5 py-0.5 text-xs text-zinc-500">
            réglé — {queue.default_max_concurrent} par défaut
          </span>
        )}

        {!queue.known && (
          // Une tâche locale : elle ne devient pas un PipelineRun, mais occupe
          // bien une file. Le dire évite de la chercher dans le catalogue.
          <span className="rounded border border-zinc-200 px-1.5 py-0.5 text-xs text-zinc-500">
            tâche interne
          </span>
        )}

        {queue.waiting > 0 && (
          <span className="ml-auto text-xs text-zinc-500">{queue.waiting} en attente</span>
        )}
      </div>

      {queue.jobs.length === 0 ? (
        <EmptyState>Cette file est vide.</EmptyState>
      ) : (
        <ul className="flex flex-col gap-2">
          {/* L'ordre est celui du service, pas un autre : une file affichée dans
              un ordre différent de celui où elle sera servie fait attendre le
              mauvais job. */}
          {queue.jobs.map((job, index) => (
            <JobRow
              key={job.id}
              job={job}
              queue={queue}
              canCancel={canCancel}
              rank={job.state === 'queued' ? index + 1 - queue.running : undefined}
            />
          ))}
        </ul>
      )}
    </Card>
  )
}

function JobRow({
  job,
  queue,
  canCancel,
  rank,
}: {
  job: Job
  queue: Queue
  canCancel: boolean
  rank?: number
}) {
  const cancel = useCancel()

  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border border-zinc-200 px-3 py-2">
      {rank !== undefined && (
        <span className="w-6 shrink-0 text-right text-xs tabular-nums text-zinc-400">{rank}</span>
      )}

      {job.attempt > 1 && (
        // Un job qui a déjà échoué à démarrer mérite d'être signalé : c'est le
        // signe d'un problème côté cluster, pas d'une attente ordinaire.
        <span className="rounded border border-warn-500 bg-warn-50 px-1.5 py-0.5 text-xs text-warn-700">
          essai {job.attempt}
        </span>
      )}

      {job.state === 'running' && job.run_name && (
        <Link
          to={`/runs/${encodeURIComponent(job.run_name)}`}
          className="font-mono text-xs text-zinc-600 hover:underline"
        >
          {job.run_name}
        </Link>
      )}

      {job.state === 'queued' && (
        <span className="flex items-center gap-1.5 text-xs text-zinc-500">
          <Icon name="clock" className="size-3.5" />
          {explainWait(job, queue)}
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
