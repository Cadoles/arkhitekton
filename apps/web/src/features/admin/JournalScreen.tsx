import { useState } from 'react'
import { Link } from 'react-router'
import { Card, EmptyState, Skeleton } from '../../components/primitives'
import { Select } from '../../components/Select'
import { formatDateTime } from '../../lib/format'
import { useInterface } from '../../lib/interface'
import { ACTION_LABEL, useProjects } from './api'
import {
  EVENT_LABEL,
  automationLabel,
  outcomeOf,
  useJournal,
  type JournalAutomation,
  type JournalEntry,
  type JournalFilter,
} from './journal'

/*
 * Le journal de l'application : les actions (qui a fait quoi) et les événements
 * (ce qui s'est passé), dans un seul fil, le plus récent d'abord.
 *
 * Réservé aux administrateurs : les actions couvrent toute l'instance, et la
 * charge d'un événement peut porter celle d'une forge tierce.
 */

const PAGE = 100

export function JournalScreen() {
  const [filter, setFilter] = useState<JournalFilter>({ nature: '', project: '', search: '', limit: PAGE })
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
        <strong className="font-medium">événement</strong> dit ce qui s&apos;est passé, et quelles automatisations
        y ont réagi. Les actions faites par un plugin ne sont pas encore rattachées à leur projet.
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
                onClick={() => setFilter((current) => ({ ...current, limit: current.limit + PAGE }))}
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
      {entry.nature === 'action' ? <ActionLine entry={entry} /> : <EventLine entry={entry} />}
      {entry.project && (
        <span className="rounded border border-zinc-300 px-1.5 py-0.5 text-xs text-zinc-600">{entry.project}</span>
      )}
      <span className="ml-auto text-xs text-zinc-500">{formatDateTime(entry.at)}</span>
      {entry.nature === 'event' && <Reactions entry={entry} />}
    </li>
  )
}

function NatureBadge({ nature }: { nature: JournalEntry['nature'] }) {
  return nature === 'action' ? (
    <span className="w-20 shrink-0 rounded border border-zinc-400 px-1.5 py-0.5 text-center text-xs text-zinc-700">
      Action
    </span>
  ) : (
    <span className="w-20 shrink-0 rounded border border-zinc-200 bg-zinc-50 px-1.5 py-0.5 text-center text-xs text-zinc-600">
      Événement
    </span>
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

/** Ce qui a réagi à un événement : la réponse à « pourquoi rien ne s'est passé ? ». */
function Reactions({ entry }: { entry: JournalEntry }) {
  const automations = entry.automations ?? []
  let content
  if (entry.pending) {
    content = <span className="text-zinc-500">En attente de routage vers les automatisations.</span>
  } else if (automations.length === 0) {
    content = <span className="text-zinc-400">Aucune automatisation n&apos;a réagi.</span>
  } else {
    content = (
      <>
        <span className="text-zinc-500">A déclenché</span>
        {automations.map((automation, index) => (
          <AutomationChip key={index} automation={automation} project={entry.project} />
        ))}
      </>
    )
  }
  return <div className="flex w-full flex-wrap items-center gap-1.5 pl-[5.75rem] text-xs">{content}</div>
}

const STATE: Record<JournalAutomation['state'], { label: string; tone: string }> = {
  running: { label: 'en cours', tone: 'border-zinc-400 text-zinc-700' },
  succeeded: { label: 'réussie', tone: 'border-healthy-500 bg-healthy-50 text-healthy-700' },
  failed: { label: 'échouée', tone: 'border-critical-500 bg-critical-50 text-critical-700' },
}

function AutomationChip({ automation, project }: { automation: JournalAutomation; project?: string }) {
  const state = STATE[automation.state] ?? { label: automation.state, tone: 'border-zinc-300 text-zinc-600' }
  const chip = (
    <span className={`inline-flex items-center gap-1 rounded border px-1.5 py-0.5 ${state.tone}`}>
      {automationLabel(automation)} · {state.label}
    </span>
  )
  // Le détail des étapes vit sous les exécutions du projet.
  return project ? (
    <Link to={`/runs/automations?project=${encodeURIComponent(project)}`} className="hover:underline">
      {chip}
    </Link>
  ) : (
    chip
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
