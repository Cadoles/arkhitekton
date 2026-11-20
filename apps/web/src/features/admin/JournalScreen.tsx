import { useState, type ReactNode } from 'react'
import { Link, useSearchParams } from 'react-router'
import { Icon } from '../../components/Icon'
import { Card, EmptyState, Skeleton } from '../../components/primitives'
import { Select } from '../../components/Select'
import { formatDateTime } from '../../lib/format'
import { useInterface } from '../../lib/interface'
import { ACTION_LABEL, useProjects } from './api'
import {
  EVENT_LABEL,
  STEP_LABEL,
  automationLabel,
  outcomeOf,
  useJournal,
  type JournalAutomation,
  type JournalEntry,
  type JournalFilter,
  type JournalNature,
  type PlanStep,
} from './journal'

/*
 * Le journal de l'application : les actions (qui a fait quoi), les événements
 * (ce qui s'est passé) et les automatisations qu'ils ont déclenchées, dans un
 * seul fil, le plus récent d'abord.
 *
 * Réservé aux administrateurs : les actions couvrent toute l'instance, la charge
 * d'un événement peut porter celle d'une forge tierce, et le détail d'un geste ce
 * qu'un destinataire a répondu.
 */

const PAGE = 100
const NATURES: JournalNature[] = ['action', 'event', 'automation']

export function JournalScreen() {
  // L'ancien onglet des automatisations mène ici, filtré.
  const [params] = useSearchParams()
  const [filter, setFilter] = useState<JournalFilter>(() => {
    const nature = params.get('nature') as JournalNature | null
    return {
      nature: nature && NATURES.includes(nature) ? nature : '',
      project: params.get('project') ?? '',
      search: '',
      limit: PAGE,
    }
  })
  const [search, setSearch] = useState('')
  const { data, isPending, error } = useJournal(filter)
  const projects = useProjects()

  const update = (change: Partial<JournalFilter>) => setFilter((current) => ({ ...current, limit: PAGE, ...change }))

  return (
    <Card
      title="Journal"
      action={
        <form
          className="flex flex-wrap items-center gap-2"
          onSubmit={(event) => {
            event.preventDefault()
            update({ search: search.trim() })
          }}
        >
          <Select
            size="xs"
            value={filter.nature}
            aria-label="Nature des entrées"
            onChange={(event) => update({ nature: event.target.value as JournalFilter['nature'] })}
          >
            <option value="">Tout</option>
            <option value="action">Actions</option>
            <option value="event">Événements</option>
            <option value="automation">Automatisations</option>
          </Select>
          <Select
            size="xs"
            value={filter.project}
            aria-label="Projet"
            onChange={(event) => update({ project: event.target.value })}
          >
            <option value="">Tous les projets</option>
            {(projects.data?.projects ?? []).map((project) => (
              <option key={project.slug} value={project.slug}>
                {project.label}
              </option>
            ))}
          </Select>
          <input
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder="rechercher…"
            aria-label="Rechercher dans le journal"
            className="rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-xs text-zinc-900"
          />
          <button
            type="submit"
            className="rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
          >
            Rechercher
          </button>
        </form>
      }
    >
      <p className="mb-3 text-sm text-zinc-600">
        Une <strong className="font-medium">action</strong> dit qui a fait quoi ; un{' '}
        <strong className="font-medium">événement</strong> dit ce qui s&apos;est passé, et quelles automatisations y ont
        réagi : cliquer sur l&apos;une montre ses étapes. Une planification apparaît seule. Les actions faites par un
        plugin ne sont pas encore rattachées à leur projet.
      </p>

      {isPending && <Skeleton className="h-32 w-full" />}
      {error && <EmptyState>Le journal n&apos;a pas pu être lu : {(error as Error).message}</EmptyState>}

      {data &&
        (data.entries.length === 0 ? (
          <EmptyState>Rien ne correspond à ces filtres.</EmptyState>
        ) : (
          <>
            <ul className="flex flex-col divide-y divide-zinc-200">
              {data.entries.map((entry) => (
                <JournalRow key={`${entry.nature}-${entry.id}`} entry={entry} />
              ))}
            </ul>
            {data.entries.length >= filter.limit && (
              <button
                type="button"
                onClick={() =>
                  setFilter((current) => ({
                    ...current,
                    limit: current.limit + PAGE,
                  }))
                }
                className="mt-3 rounded-md border border-zinc-300 bg-white px-3 py-1 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
              >
                Plus ancien
              </button>
            )}
          </>
        ))}
    </Card>
  )
}

function JournalRow({ entry }: { entry: JournalEntry }) {
  return (
    <li className="flex flex-wrap items-baseline gap-x-3 gap-y-1 py-2 text-sm">
      <NatureBadge nature={entry.nature} />
      {entry.nature === 'action' ? (
        <ActionLine entry={entry} />
      ) : entry.nature === 'event' ? (
        <EventLine entry={entry} />
      ) : (
        <RunLine entry={entry} />
      )}
      {entry.project && (
        <span className="rounded border border-zinc-300 px-1.5 py-0.5 text-xs text-zinc-600">{entry.project}</span>
      )}
      <span className="ml-auto text-xs text-zinc-500">{formatDateTime(entry.at)}</span>
      {entry.nature === 'event' && <Reactions entry={entry} />}
      {entry.nature === 'automation' && <Runs runs={entry.automations ?? []} lead="Exécution" />}
    </li>
  )
}

const NATURE: Record<JournalNature, { label: string; tone: string }> = {
  action: { label: 'Action', tone: 'border-zinc-400 text-zinc-700' },
  event: {
    label: 'Événement',
    tone: 'border-zinc-200 bg-zinc-50 text-zinc-600',
  },
  automation: {
    label: 'Automatisation',
    tone: 'border-zinc-200 bg-zinc-50 text-zinc-600',
  },
}

function NatureBadge({ nature }: { nature: JournalNature }) {
  const { label, tone } = NATURE[nature]
  return (
    <span className={`w-24 shrink-0 truncate rounded border px-1.5 py-0.5 text-center text-xs ${tone}`}>{label}</span>
  )
}

function ActionLine({ entry }: { entry: JournalEntry }) {
  const action = entry.action ?? ''
  return (
    <>
      <span className="font-medium text-zinc-900">{ACTION_LABEL[action] ?? action}</span>
      {entry.target && <span className="font-mono text-zinc-600">{entry.target}</span>}
      <span className="text-zinc-500">par {entry.actor}</span>
      {entry.payload && Object.keys(entry.payload).length > 0 && (
        <span className="text-xs text-zinc-500">
          {Object.entries(entry.payload)
            .map(([key, value]) => `${key}=${typeof value === 'string' ? value : JSON.stringify(value)}`)
            .join(' ')}
        </span>
      )}
    </>
  )
}

function EventLine({ entry }: { entry: JournalEntry }) {
  // Les événements d'un plugin arrivent avec ses libellés, que son manifeste
  // déclare ; lus même plugin éteint : le journal est une histoire.
  const { eventLabels } = useInterface()
  const type = entry.type ?? ''
  const outcome = outcomeOf(entry)

  return (
    <>
      <span className="font-medium text-zinc-900" title={type}>
        {EVENT_LABEL[type] ?? eventLabels[type] ?? type}
      </span>
      {entry.subject && <span className="font-mono text-zinc-600">{entry.subject}</span>}
      {outcome && <OutcomeBadge outcome={outcome} />}
      {/* Le plugin qui l'a constaté, distinct du projet : `mse` est un plugin et un
          projet sur la même instance. */}
      {entry.source && <span className="text-xs text-zinc-500">via {entry.source}</span>}
      {entry.recorded_at && (
        <span className="text-xs text-zinc-400">constaté le {formatDateTime(entry.recorded_at)}</span>
      )}
    </>
  )
}

/** Une exécution qu'aucun événement n'a déclenchée : le tic d'une planification. */
function RunLine({ entry }: { entry: JournalEntry }) {
  const run = entry.automations?.[0]
  if (!run) return null
  return (
    <>
      <span className="font-medium text-zinc-900" title={run.trigger}>
        {automationLabel(run)}
      </span>
      {run.trigger.startsWith('schedule:') && <span className="text-zinc-500">planifiée</span>}
    </>
  )
}

/** Ce qui a réagi à un événement : la réponse à « pourquoi rien ne s'est passé ? ». */
function Reactions({ entry }: { entry: JournalEntry }) {
  const automations = entry.automations ?? []
  if (entry.pending) {
    return (
      <Indented>
        <span className="text-zinc-500">En attente de routage vers les automatisations.</span>
      </Indented>
    )
  }
  if (automations.length === 0) {
    return (
      <Indented>
        <span className="text-zinc-400">Aucune automatisation n&apos;a réagi.</span>
      </Indented>
    )
  }
  return <Runs runs={automations} lead="A déclenché" />
}

function Indented({ children }: { children: ReactNode }) {
  return <div className="flex w-full flex-wrap items-center gap-1.5 text-xs sm:pl-[6.75rem]">{children}</div>
}

/** Les exécutions d'une ligne, chacune dépliable sur ses étapes. */
function Runs({ runs, lead }: { runs: JournalAutomation[]; lead: string }) {
  const [open, setOpen] = useState<number | null>(null)
  const shown = runs.find((run) => run.id === open)
  return (
    <>
      <Indented>
        <span className="text-zinc-500">{lead}</span>
        {runs.map((run) => (
          <AutomationChip
            key={run.id}
            automation={run}
            open={run.id === open}
            onToggle={() => setOpen((current) => (current === run.id ? null : run.id))}
          />
        ))}
      </Indented>
      {shown && <RunDetail run={shown} />}
    </>
  )
}

const STATE: Record<JournalAutomation['state'], { label: string; tone: string }> = {
  running: { label: 'en cours', tone: 'border-zinc-400 text-zinc-700' },
  succeeded: {
    label: 'réussie',
    tone: 'border-healthy-500 bg-healthy-50 text-healthy-700',
  },
  failed: {
    label: 'échouée',
    tone: 'border-critical-500 bg-critical-50 text-critical-700',
  },
}

function AutomationChip({
  automation,
  open,
  onToggle,
}: {
  automation: JournalAutomation
  open: boolean
  onToggle: () => void
}) {
  const state = STATE[automation.state] ?? {
    label: automation.state,
    tone: 'border-zinc-300 text-zinc-600',
  }
  return (
    <button
      type="button"
      aria-expanded={open}
      onClick={onToggle}
      className={`inline-flex items-center gap-1 rounded border px-1.5 py-0.5 transition-colors hover:brightness-95 ${state.tone}`}
    >
      {automationLabel(automation)} · {state.label}
      <Icon name="chevron" className={`size-3 transition-transform ${open ? 'rotate-180' : ''}`} />
    </button>
  )
}

/** Les étapes d'une exécution : des étapes empilées, des gestes en colonnes. */
function RunDetail({ run }: { run: JournalAutomation }) {
  return (
    <div className="w-full sm:pl-[6.75rem]">
      <div className="flex flex-col gap-2 rounded-md border border-zinc-200 bg-zinc-50 p-2 text-xs">
        {/* Le sujet du fait déclencheur : sans lui, deux exécutions se ressemblent. */}
        {run.context.tag && <span className="font-mono text-zinc-700">{run.context.tag}</span>}
        <ol className="flex flex-col gap-2">
          {run.stages.map((stage, index) => (
            <li key={index} className="flex flex-wrap items-start gap-2">
              <span className="w-20 shrink-0 text-zinc-500">{stage.label ?? `Étape ${index + 1}`}</span>
              {stage.steps.length === 0 ? (
                <span className="text-zinc-400">aucun geste</span>
              ) : (
                stage.steps.map((step) => <StepChip key={`${step.stage}:${step.position}`} step={step} />)
              )}
            </li>
          ))}
        </ol>
      </div>
    </div>
  )
}

const STEP_TONE: Record<PlanStep['state'], string> = {
  succeeded: 'border-healthy-500 bg-healthy-50 text-healthy-700',
  failed: 'border-critical-500 bg-critical-50 text-critical-700',
  running: 'border-zinc-400 bg-white text-zinc-800',
  pending: 'border-zinc-200 bg-white text-zinc-500',
  skipped: 'border-zinc-200 bg-white text-zinc-500',
}

function StepChip({ step }: { step: PlanStep }) {
  return (
    <span className="flex flex-col gap-0.5">
      <span className={`rounded border px-1.5 py-0.5 ${STEP_TONE[step.state]}`}>
        {step.label || step.ref}
        <span className="ml-1 text-[10px] opacity-75">{STEP_LABEL[step.state]}</span>
      </span>
      {/* Le geste de pipeline mène à son exécution : c'est là que vivent les journaux. */}
      {step.job_id !== undefined && step.kind === 'pipeline' && (
        <Link to="/runs/queue" className="text-[10px] text-zinc-500 hover:underline">
          job n° {step.job_id}
        </Link>
      )}
      {step.detail && <span className="max-w-72 text-[10px] break-words text-zinc-500">{step.detail}</span>}
    </span>
  )
}

/** L'issue d'un événement, quand la question se pose. */
function OutcomeBadge({ outcome }: { outcome: string }) {
  const tone =
    outcome === 'succeeded'
      ? 'border-healthy-500 bg-healthy-50 text-healthy-700'
      : outcome === 'cancelled'
        ? 'border-zinc-300 bg-zinc-50 text-zinc-600'
        : 'border-critical-500 bg-critical-50 text-critical-700'
  const label = outcome === 'succeeded' ? 'réussi' : outcome === 'cancelled' ? 'annulé' : 'échoué'
  return <span className={`rounded border px-1.5 py-0.5 text-xs ${tone}`}>{label}</span>
}
