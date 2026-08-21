import { useMemo, useState } from 'react'
import { Link, useParams } from 'react-router'
import { Icon } from '../../components/Icon'
import { Card, EmptyState, ErrorState, Skeleton, StatusBadge } from '../../components/primitives'
import { formatDateTime } from '../../lib/format'
import { ApiError } from '../../lib/api'
import { duration, formatDuration, isTerminal, useRun, type Task } from './api'
import { runStatus } from './status'
import { useRunEvents } from './useRunEvents'
import { LogViewer } from './LogViewer'

/*
 * Détail d'une exécution : ses tasks, et le journal de celle qu'on sélectionne.
 *
 * L'ordre des tasks est celui de leur démarrage, les non démarrées en fin de
 * liste. Le graphe des dépendances `runAfter` n'est pas encore affiché — il
 * demande de lire la définition du Pipeline, ce que le lot 3 apportera avec le
 * catalogue.
 */
export function RunScreen() {
  const { name = '' } = useParams()
  const { data, isPending, error, refetch } = useRun(name)
  const [selected, setSelected] = useState<string | null>(null)

  // Le flux d'état invalide le détail à chaque changement : les tasks
  // apparaissent et changent de statut sans intervention.
  useRunEvents()

  const tasks = useMemo(() => data?.tasks ?? [], [data])

  // Sélection par défaut : la première task en échec, sinon la dernière
  // démarrée. C'est ce qu'on vient voir — on ouvre un run pour comprendre
  // pourquoi il a échoué, pas pour relire ses étapes réussies.
  //
  // Dérivée plutôt que stockée par un effet : la valeur suit alors les tasks qui
  // apparaissent au fil de l'exécution, sans rendu supplémentaire, et un choix
  // explicite de l'utilisateur la remplace simplement.
  const defaultTask = useMemo(() => {
    const failed = tasks.find((task) => task.status === 'failed' || task.status === 'timedout')
    return failed?.name ?? tasks[tasks.length - 1]?.name ?? null
  }, [tasks])

  const activeTask = selected ?? defaultTask

  if (isPending) {
    return (
      <div className="flex flex-col gap-4">
        <Skeleton className="h-8 w-96" />
        <Skeleton className="h-40 w-full" />
      </div>
    )
  }

  if (error) {
    const missing = error instanceof ApiError && error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <h1 className="font-mono text-xl text-zinc-900">{name}</h1>
        {missing ? (
          <EmptyState
            action={
              <Link to="/runs" className="text-sm font-medium text-zinc-900 hover:underline">
                Revenir aux exécutions
              </Link>
            }
          >
            {/* Un run purgé n'est pas une panne : les CronJobs de nettoyage de
                `mse-tekton` en suppriment régulièrement. */}
            Cette exécution n'existe plus. Elle a pu être purgée du cluster.
          </EmptyState>
        ) : (
          <ErrorState message="L'exécution n'a pas pu être chargée." onRetry={() => void refetch()} />
        )}
      </div>
    )
  }

  const run = data.run
  const style = runStatus(run.status)
  const current = tasks.find((task) => task.name === activeTask) ?? null

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-2">
        <Link to="/runs" className="flex w-fit items-center gap-1 text-xs text-zinc-500 hover:text-zinc-900">
          <Icon name="arrow-right" className="size-3.5 rotate-180" />
          Exécutions
        </Link>

        <div className="flex flex-wrap items-center gap-3">
          <h1 className="font-mono text-xl text-zinc-900">{run.name}</h1>
          <StatusBadge style={style} />
          {run.observed && (
            <span className="rounded border border-zinc-200 px-1.5 py-0.5 text-xs text-zinc-500">
              observée — non lancée par le dashboard
            </span>
          )}

          {/* Le lien vers Tekton passe en tête plutôt qu'en métadonnée : c'est
              une sortie, pas une donnée à lire. Absent quand le dashboard Tekton
              n'est pas configuré. */}
          {run.dashboard_url && (
            <a
              href={run.dashboard_url}
              target="_blank"
              rel="noreferrer"
              className="ml-auto inline-flex items-center gap-1.5 rounded-md border border-zinc-300 bg-white px-2 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
            >
              <Icon name="external" className="size-3.5" />
              Ouvrir dans Tekton
            </a>
          )}
        </div>
      </div>

      <Card>
        <dl className="flex flex-wrap gap-x-8 gap-y-3">
          <Field label="Pipeline" value={run.pipeline} />
          <Field label="Début" value={run.started_at ? formatDateTime(run.started_at) : '—'} />
          <Field label="Durée" value={formatDuration(duration(run.started_at, run.completed_at))} />

          {/* La provenance, quand elle existe. La release prime : une exécution
              de vérification appartient à ce qu'elle vérifie, et savoir qui a
              cliqué importe moins. Un run observé n'a ni l'une ni l'autre —
              rien ne s'affiche alors, plutôt qu'un tiret qui se lirait comme une
              donnée perdue. */}
          {run.release_tag && (
            <div>
              <dt className="text-xs text-zinc-500">Release</dt>
              <dd className="text-sm">
                <Link
                  to={`/releases/${encodeURIComponent(run.release_tag)}`}
                  className="font-mono text-zinc-900 hover:underline"
                >
                  {run.release_tag}
                </Link>
              </dd>
            </div>
          )}
          {run.triggered_by && <Field label="Demandée par" value={run.triggered_by} />}

          {run.reason && <Field label="Motif Tekton" value={run.reason} mono />}
        </dl>
      </Card>

      {tasks.length === 0 ? (
        <EmptyState>Aucune task n'a encore été créée pour cette exécution.</EmptyState>
      ) : (
        <div className="grid gap-4 lg:grid-cols-[18rem_1fr]">
          <Card title="Tasks" className="h-fit">
            <ul className="flex flex-col gap-1">
              {tasks.map((task) => (
                <TaskRow
                  key={task.name}
                  task={task}
                  active={task.name === activeTask}
                  onSelect={() => setSelected(task.name)}
                />
              ))}
            </ul>
          </Card>

          <Card title={current ? `Journal — ${current.display_name}` : 'Journal'}>
            {current ? (
              current.has_log ? (
                <LogViewer
                  // La clé force un remontage au changement de task : sans elle,
                  // la visionneuse garderait la position et le contenu de la
                  // précédente le temps du chargement.
                  key={current.name}
                  runName={run.name}
                  taskName={current.name}
                  sealed={current.log_sealed}
                />
              ) : (
                <EmptyState>
                  {isTerminal(current.status)
                    ? "Aucun journal n'a été capté pour cette task."
                    : "Le journal apparaîtra dès que la task aura démarré."}
                </EmptyState>
              )
            ) : (
              <EmptyState>Sélectionnez une task.</EmptyState>
            )}
          </Card>
        </div>
      )}
    </div>
  )
}

function TaskRow({ task, active, onSelect }: { task: Task; active: boolean; onSelect: () => void }) {
  const style = runStatus(task.status)

  return (
    <li>
      <button
        type="button"
        onClick={onSelect}
        aria-current={active ? 'true' : undefined}
        className={`flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm transition-colors ${
          active ? 'bg-zinc-100 text-zinc-900' : 'text-zinc-600 hover:bg-zinc-50'
        }`}
      >
        <Icon name={style.icon} className={`size-4 shrink-0 ${style.text}`} />
        <span className="flex-1 truncate">{task.display_name}</span>
        <span className="shrink-0 text-xs tabular-nums text-zinc-400">
          {formatDuration(duration(task.started_at, task.completed_at))}
        </span>
      </button>
    </li>
  )
}

function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div>
      <dt className="text-xs text-zinc-500">{label}</dt>
      <dd className={`text-sm text-zinc-900 ${mono ? 'font-mono text-xs' : ''}`}>{value}</dd>
    </div>
  )
}
