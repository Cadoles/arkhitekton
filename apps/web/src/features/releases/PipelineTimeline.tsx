import { Icon } from '../../components/Icon'
import { isTerminal, useRun, type RunStatus, type Task } from '../runs/api'

/*
 * Frise des étapes d'un pipeline en cours.
 *
 * Une barre de progression indéterminée ne dit que « ça tourne ». Un pipeline
 * d'intégration met vingt minutes et enchaîne clonage, déploiement, bootstrap,
 * tests : savoir **où** il en est change ce qu'on décide d'en faire — attendre,
 * ouvrir les journaux, ou l'abandonner.
 *
 * L'étape en cours est la seule animée. Tout faire clignoter donnerait un mur
 * d'agitation où l'on ne repère plus rien.
 */

const TASK_TONE: Record<RunStatus, string> = {
  succeeded: 'border-healthy-500 bg-healthy-50 text-healthy-700',
  failed: 'border-critical-500 bg-critical-50 text-critical-700',
  cancelled: 'border-zinc-300 bg-zinc-100 text-zinc-500',
  timedout: 'border-warn-500 bg-warn-50 text-warn-700',
  running: 'border-zinc-400 bg-white text-zinc-900',
  pending: 'border-dashed border-zinc-300 bg-white text-zinc-400',
}

export function PipelineTimeline({ runName }: { runName: string }) {
  const { data } = useRun(runName)

  if (!data) return null

  // L'ordre vient du serveur, qui rend celui du graphe : les tâches déclarées
  // dans leur ordre de définition, les tâches de fin en dernier. Retrier ici par
  // date de démarrage rejetterait les non démarrées à la fin de la frise — elles
  // n'ont pas de date — alors que c'est justement leur place dans la suite qu'on
  // vient lire.
  const tasks = data.tasks

  if (tasks.length === 0) {
    return (
      <p className="flex items-center gap-2 rounded-md border border-zinc-200 px-3 py-2 text-sm text-zinc-500">
        <Spark />
        Exécution démarrée, aucune étape encore ordonnancée.
      </p>
    )
  }

  return (
    <div className="rounded-md border border-zinc-200 p-3">
      <p className="mb-2 flex items-center gap-2 text-xs text-zinc-500">
        <Spark />
        <span className="font-mono">{runName}</span>
        {!isTerminal(data.run.status) && <span>— exécution en cours</span>}
      </p>

      {/* Défilement horizontal plutôt que retour à la ligne : une frise qui se
          replie sur trois lignes cesse de se lire comme une chronologie. */}
      <ol className="flex items-stretch gap-1 overflow-x-auto pb-1">
        {tasks.map((task, index) => (
          <li key={task.name} className="flex items-center gap-1">
            {index > 0 && <Icon name="arrow-right" className="size-3 shrink-0 text-zinc-300" />}
            <TaskChip task={task} />
          </li>
        ))}
      </ol>
    </div>
  )
}

function TaskChip({ task }: { task: Task }) {
  const running = task.status === 'running'

  return (
    <span
      className={`flex shrink-0 items-center gap-1.5 whitespace-nowrap rounded-md border px-2 py-1 text-xs font-medium ${
        TASK_TONE[task.status]
      } ${running ? 'animate-pulse' : ''}`}
      title={task.display_name || task.name}
    >
      {task.status === 'succeeded' && <Icon name="check" className="size-3" />}
      {task.status === 'failed' && <Icon name="cross" className="size-3" />}
      {task.status === 'timedout' && <Icon name="clock" className="size-3" />}
      {running && <Spark />}
      {task.display_name || task.name}
    </span>
  )
}

/** Point qui bat. Assez discret pour figurer dans une pastille de texte. */
function Spark() {
  return (
    <span className="relative flex size-2 shrink-0" aria-hidden="true">
      <span className="absolute inline-flex size-full animate-ping rounded-full bg-zinc-400 opacity-75" />
      <span className="relative inline-flex size-2 rounded-full bg-zinc-500" />
    </span>
  )
}
