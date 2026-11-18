import { useMemo } from 'react'
import { Link, useParams } from 'react-router'
import { CollapsibleSection } from '../../components/CollapsibleSection'
import { Icon } from '../../components/Icon'
import { EmptyState, ErrorState, Skeleton, StatusBadge } from '../../components/primitives'
import { formatDateTime } from '../../lib/format'
import { status } from '../../lib/status'
import { JobError } from '../releases/JobError'
import { LaunchPipelineButton } from '../releases/LaunchPipeline'
import { TestsPanel } from '../releases/TestsPanel'
import { pullSubject } from '../releases/tests'
import { usePull, type PullDetail } from './api'
import { ReviewSection } from './ReviewSection'

/*
 * La page d'une pull request, sur le modèle de celle d'une release : ce qu'elle est, ce qu'on y
 * lance, ce que les résultats disent, ce qui a tourné.
 *
 * Les résultats sont ceux des pipelines lancés sur la PR, à la main ou par une automatisation
 * sur ses faits. Chaque famille se compare à la poussée précédente de la même PR.
 */

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
            <LaunchPipelineButton
              subject={subject}
              disabled={!pull.head_sha}
              disabledReason="La forge ne donne aucun commit pour cette pull request"
            />
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

      <ReviewSection number={pull.number} subject={subject} />

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
                {job.error && <JobError error={job.error} />}
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
