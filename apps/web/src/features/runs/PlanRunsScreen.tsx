import { Link } from 'react-router'
import { Card, EmptyState, Skeleton } from '../../components/primitives'
import { formatDateTime } from '../../lib/format'
import {
  STEP_LABEL,
  triggerLabel,
  usePlanRuns,
  type PlanRun,
  type PlanStep,
} from './planruns'

/*
 * Les exécutions d'automatisation.
 *
 * **Un moteur qu'on ne peut pas déboguer est pire qu'un comportement figé**,
 * parce qu'on ne sait plus ce qui a eu lieu. Cet écran répond à la question que
 * l'éditeur laisse ouverte : « ma réaction est partie, qu'a-t-elle fait ? »
 *
 * La forme du modèle se dessine directement : des étapes empilées, des gestes en
 * colonnes. C'est la seconde raison d'avoir refusé un graphe libre — celui-ci
 * n'aurait pas eu de représentation lisible.
 */
export function PlanRunsScreen() {
  const { data, isPending, error } = usePlanRuns()

  return (
    <Card title="Exécutions d'automatisation">
      {isPending && <Skeleton className="h-32 w-full" />}

      {error && (
        <EmptyState>Les exécutions n'ont pas pu être lues : {(error as Error).message}</EmptyState>
      )}

      {data && (
        <div className="flex flex-col gap-3">
          {/* Dit ce qui est masqué plutôt que de le taire : sans cela, un lecteur
              non administrateur croirait qu'il n'y a rien à voir. */}
          {!data.details_visible && (
            <p className="text-xs text-zinc-500">
              Le détail des appels sortants — ce qu'un destinataire a répondu — est réservé à
              l'administration.
            </p>
          )}

          {data.runs.length === 0 ? (
            <EmptyState>
              Aucune exécution. Elles apparaissent quand une release se construit, ou quand
              une réaction déclarée se déclenche.
            </EmptyState>
          ) : (
            <ul className="flex flex-col gap-3">
              {data.runs.map((run) => (
                <PlanRunCard key={run.id} run={run} />
              ))}
            </ul>
          )}
        </div>
      )}
    </Card>
  )
}

function PlanRunCard({ run }: { run: PlanRun }) {
  return (
    <li className="rounded-md border border-zinc-200 p-3">
      <div className="mb-2 flex flex-wrap items-baseline gap-x-2 gap-y-1">
        <span className="text-sm font-medium text-zinc-900">{triggerLabel(run.trigger)}</span>
        <RunStateBadge state={run.state} />

        {/* Le sujet du fait déclencheur : c'est lui qui dit sur quoi l'exécution a
            porté, et sans lui deux exécutions se ressemblent. */}
        {run.context.tag && (
          <span className="font-mono text-xs text-zinc-700">{run.context.tag}</span>
        )}

        <span className="ml-auto text-xs text-zinc-500">{formatDateTime(run.created_at)}</span>
      </div>

      <ol className="flex flex-col gap-2">
        {run.stages.map((stage, index) => (
          <li key={index} className="flex flex-wrap items-center gap-2">
            <span className="w-20 shrink-0 text-xs text-zinc-500">
              {stage.label ?? `Étape ${index + 1}`}
            </span>
            {stage.steps.length === 0 ? (
              <span className="text-xs text-zinc-400">aucun geste</span>
            ) : (
              stage.steps.map((step) => (
                <StepChip key={`${step.stage}:${step.position}`} step={step} />
              ))
            )}
          </li>
        ))}
      </ol>
    </li>
  )
}

function StepChip({ step }: { step: PlanStep }) {
  const tone =
    step.state === 'succeeded'
      ? 'border-healthy-500 bg-healthy-50 text-healthy-700'
      : step.state === 'failed'
        ? 'border-critical-500 bg-critical-50 text-critical-700'
        : step.state === 'running'
          ? 'border-zinc-400 bg-white text-zinc-800'
          : 'border-zinc-200 bg-zinc-50 text-zinc-500'

  return (
    <span className="flex flex-col gap-0.5">
      <span className={`rounded border px-1.5 py-0.5 text-xs ${tone}`}>
        {step.label || step.ref}
        <span className="ml-1 text-[10px] opacity-75">{STEP_LABEL[step.state]}</span>
      </span>

      {/* Le geste de pipeline mène à son exécution : c'est là que vivent les
          journaux, et y arriver depuis ici évite de chercher par le nom du run. */}
      {step.job_id !== undefined && step.kind === 'pipeline' && (
        <Link to="/runs/queue" className="text-[10px] text-zinc-500 hover:underline">
          job n° {step.job_id}
        </Link>
      )}

      {step.detail && (
        <span className="max-w-72 text-[10px] break-words text-zinc-500">{step.detail}</span>
      )}
      {step.detail_withheld && (
        <span className="text-[10px] text-zinc-400">détail réservé à l'administration</span>
      )}
    </span>
  )
}

function RunStateBadge({ state }: { state: PlanRun['state'] }) {
  const tone =
    state === 'succeeded'
      ? 'border-healthy-500 bg-healthy-50 text-healthy-700'
      : state === 'failed'
        ? 'border-critical-500 bg-critical-50 text-critical-700'
        : 'border-zinc-300 bg-white text-zinc-700'

  const label = state === 'succeeded' ? 'conclue' : state === 'failed' ? 'échouée' : 'en cours'

  return <span className={`rounded border px-1.5 py-0.5 text-xs ${tone}`}>{label}</span>
}
