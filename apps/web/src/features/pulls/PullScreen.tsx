import { useMemo, useState } from 'react'
import { Link, useParams } from 'react-router'
import { CollapsibleSection } from '../../components/CollapsibleSection'
import { Icon } from '../../components/Icon'
import { Modal } from '../../components/Modal'
import { EmptyState, ErrorState, Skeleton, StatusBadge } from '../../components/primitives'
import { formatDateTime } from '../../lib/format'
import { status } from '../../lib/status'
import { hasAtLeast, useSession } from '../auth/session'
import { useQueue, type Kind } from '../queue/api'
import { TestsPanel } from '../releases/TestsPanel'
import { pullSubject } from '../releases/tests'
import { useLaunchOnPull, usePull, type PullDetail } from './api'

/*
 * La page d'une pull request, sur le modèle de celle d'une release : ce qu'elle est, ce qu'on y
 * lance, ce que les résultats disent, ce qui a tourné.
 *
 * Les résultats sont ceux des pipelines lancés sur la PR, à la main ou par une automatisation
 * sur ses faits. Chaque famille se compare à la poussée précédente de la même PR.
 */

/** Ce que la tête de la PR remplit : la révision, l'étiquette de PR, les images construites dessous.
 *  Une image se suggère sous `image:<dépôt>`. */
function filledByHead(suggest: string | undefined): boolean {
  return suggest === 'git-ref' || suggest === 'release-tag' || suggest === 'image' || Boolean(suggest?.startsWith('image:'))
}

export function PullScreen() {
  const { number } = useParams()
  const parsed = Number(number)
  const { data, isPending, error, refetch } = usePull(Number.isFinite(parsed) ? parsed : undefined)

  if (isPending) return <Skeleton className="h-64 w-full" />
  if (error || !data) {
    return <ErrorState message="La pull request n'a pas pu être chargée." onRetry={() => void refetch()} />
  }
  return <PullPage detail={data} />
}

function PullPage({ detail }: { detail: PullDetail }) {
  const { pull } = detail
  const subject = useMemo(() => pullSubject(pull.number), [pull.number])

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-2">
        <Link to="/pulls" className="text-sm text-zinc-500 hover:underline">
          ← Pull requests
        </Link>
        <div className="flex flex-wrap items-center gap-3">
          <h1 className="text-2xl font-semibold text-zinc-900">
            <span className="font-mono text-zinc-500">#{pull.number}</span> {pull.title}
          </h1>
          <StatusBadge style={status.pull(pull.state)} />
          <span className="ml-auto">
            <LaunchButton number={pull.number} disabled={!pull.head_sha} />
          </span>
        </div>
        <p className="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-zinc-500">
          <span className="flex items-center gap-1">
            <Icon name="branch" className="size-3.5" />
            <span className="font-mono">{pull.source_branch}</span> → <span className="font-mono">{pull.target_branch}</span>
          </span>
          {pull.head_sha && (
            <span className="flex items-center gap-1">
              <Icon name="commit" className="size-3.5" />
              <span className="font-mono">{pull.head_sha.slice(0, 7)}</span>
              <span>· images sous</span>
              <span className="font-mono">{pull.tag}</span>
            </span>
          )}
          <span>{pull.author}</span>
          {pull.updated_at && <span>mise à jour {formatDateTime(pull.updated_at)}</span>}
          {pull.url && (
            <a href={pull.url} target="_blank" rel="noreferrer" className="flex items-center gap-1 hover:underline">
              <Icon name="external" className="size-3.5" />
              sur la forge
            </a>
          )}
        </p>
      </div>

      <TestsPanel subject={subject} jobs={detail.jobs} />

      <CollapsibleSection title="Exécutions" count={detail.jobs.length} defaultOpen={detail.jobs.length > 0}>
        {detail.jobs.length === 0 ? (
          <EmptyState>Rien n&apos;a encore été lancé sur cette pull request.</EmptyState>
        ) : (
          <ul className="flex flex-col gap-2">
            {detail.jobs.map((job) => (
              <li
                key={job.id}
                className="flex flex-wrap items-baseline gap-x-3 gap-y-1 rounded-md border border-zinc-200 px-3 py-2 text-sm"
              >
                <span className="font-medium text-zinc-900">{job.label || job.kind}</span>
                <span className="text-zinc-500">{job.state}</span>
                {job.run_name && (
                  <Link to={`/runs/${encodeURIComponent(job.run_name)}`} className="font-mono text-zinc-600 hover:underline">
                    {job.run_name}
                  </Link>
                )}
                <span className="ml-auto text-zinc-500">
                  {job.requested_by} — {formatDateTime(job.requested_at)}
                </span>
              </li>
            ))}
          </ul>
        )}
      </CollapsibleSection>

      <CollapsibleSection title="Automatisations" count={detail.plan_runs.length} defaultOpen={false}>
        {detail.plan_runs.length === 0 ? (
          <EmptyState>
            Aucune automatisation ne s&apos;est déclenchée sur cette pull request. Une réaction sur un fait
            de PR (ouverte, nouveaux commits, fermée, fusionnée, rouverte) se déclare dans les
            automatisations du projet.
          </EmptyState>
        ) : (
          <ul className="flex flex-col gap-2">
            {detail.plan_runs.map((run) => (
              <li key={run.id} className="flex flex-wrap items-baseline gap-x-3 gap-y-1 rounded-md border border-zinc-200 px-3 py-2 text-sm">
                <span className="font-mono text-zinc-600">{run.trigger}</span>
                <span className="text-zinc-500">{run.state}</span>
                <span className="text-zinc-500">
                  {run.stages
                    .flatMap((stage) => stage.steps)
                    .map((step) => `${step.label || step.ref} (${step.state})`)
                    .join(' · ')}
                </span>
                <span className="ml-auto text-zinc-500">{formatDateTime(run.created_at)}</span>
              </li>
            ))}
          </ul>
        )}
      </CollapsibleSection>
    </div>
  )
}

function LaunchButton({ number, disabled }: { number: number; disabled: boolean }) {
  const session = useSession()
  const [open, setOpen] = useState(false)
  const canLaunch = session.kind === 'authenticated' && hasAtLeast(session.identity.role, 'operator')
  if (!canLaunch) return null

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        disabled={disabled}
        title={disabled ? 'La forge ne donne aucun commit pour cette pull request' : undefined}
        className="flex items-center gap-1.5 rounded-md border border-zinc-900 bg-zinc-900 px-3 py-1.5 text-sm font-medium text-white transition-colors hover:bg-zinc-700 disabled:cursor-not-allowed disabled:opacity-40"
      >
        <Icon name="beaker" className="size-4" />
        Lancer un pipeline
      </button>
      {open && <LaunchModal number={number} onClose={() => setOpen(false)} />}
    </>
  )
}

function LaunchModal({ number, onClose }: { number: number; onClose: () => void }) {
  const { data } = useQueue()
  const launch = useLaunchOnPull(number)
  const kinds = useMemo(() => data?.kinds ?? [], [data])
  const [selected, setSelected] = useState('')
  const [values, setValues] = useState<Record<string, string>>({})

  const kind: Kind | undefined = kinds.find((entry) => entry.kind === selected) ?? kinds[0]
  // Ce que la tête de la PR remplit n'est pas demandé : le serveur l'impose.
  const asked = (kind?.inputs ?? []).filter((input) => !filledByHead(input.suggest))

  const submit = () => {
    if (!kind) return
    const params: Record<string, string> = {}
    for (const input of asked) {
      const value = values[input.name]?.trim()
      if (value) params[input.name] = value
    }
    launch.mutate({ kind: kind.kind, params }, { onSuccess: onClose })
  }

  return (
    <Modal title={`Lancer un pipeline sur #${number}`} onClose={onClose}>
      <form
        className="flex flex-col gap-3"
        onSubmit={(event) => {
          event.preventDefault()
          submit()
        }}
      >
        <label className="flex flex-col gap-1 text-sm">
          <span className="font-medium text-zinc-700">Pipeline</span>
          <select
            value={kind?.kind ?? ''}
            onChange={(event) => {
              setSelected(event.target.value)
              setValues({})
            }}
            className="rounded-md border border-zinc-300 px-2 py-1.5"
          >
            {kinds.map((entry) => (
              <option key={entry.kind} value={entry.kind}>
                {entry.label}
              </option>
            ))}
          </select>
        </label>

        <p className="text-xs text-zinc-500">
          La révision, l&apos;étiquette de PR et les images construites dessous sont remplies depuis la tête de la
          pull request.
        </p>

        {asked.map((input) => (
          <label key={input.name} className="flex flex-col gap-1 text-sm">
            <span className="font-medium text-zinc-700">
              {input.name}
              {input.required && <span className="text-critical-700"> *</span>}
            </span>
            <input
              value={values[input.name] ?? ''}
              onChange={(event) => setValues({ ...values, [input.name]: event.target.value })}
              list={input.suggest ? `suggest-${input.name}` : undefined}
              className="rounded-md border border-zinc-300 px-2 py-1.5 font-mono"
            />
            {input.suggest && (
              <datalist id={`suggest-${input.name}`}>
                {(data?.suggestions[input.suggest] ?? []).map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </datalist>
            )}
          </label>
        ))}

        {launch.isError && (
          <p className="flex items-start gap-1 text-sm text-critical-700">
            <Icon name="alert" className="mt-0.5 size-4 shrink-0" />
            {(launch.error as Error).message}
          </p>
        )}

        <div className="flex justify-end gap-2">
          <button type="button" onClick={onClose} className="rounded-md border border-zinc-300 px-3 py-1.5 text-sm">
            Annuler
          </button>
          <button
            type="submit"
            disabled={!kind || launch.isPending}
            className="rounded-md border border-zinc-900 bg-zinc-900 px-3 py-1.5 text-sm font-medium text-white disabled:opacity-40"
          >
            {launch.isPending ? 'Lancement…' : 'Lancer'}
          </button>
        </div>
      </form>
    </Modal>
  )
}

