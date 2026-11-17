import { useState } from 'react'
import { CollapsibleSection } from '../../components/CollapsibleSection'
import { StatusBadge } from '../../components/primitives'
import { formatDateTime } from '../../lib/format'
import { status } from '../../lib/status'
import { AttachmentList } from '../releases/AttachmentList'
import { RunLink } from '../releases/RunLink'
import { SEVERITY_LABEL, type Finding, type ResultSubject, type Severity } from '../releases/tests'
import { useReview } from './api'

/*
 * La revue IA d'une pull request : un avis, pas une mesure. Elle n'entre ni dans le score ni
 * dans la tuile Sécurité, et se lit ici, à part : le verdict du lead, son résumé, puis chaque
 * remarque à l'endroit du code qu'elle cite.
 *
 * Les remarques sur des fichiers que la PR ne touche pas restent visibles, à part : elles disent
 * quelque chose du code voisin, et ne pèsent pas sur le verdict.
 *
 * La section n'apparaît qu'une fois une revue faite : un projet qui n'en lance pas n'a pas à
 * voir une case vide.
 */

const VERDICT_LABEL: Record<string, string> = {
  approve: 'Approuvée',
  comment: 'Commentée',
  request_changes: 'Modifications demandées',
}

const SEVERITY_TONE: Record<Severity, string> = {
  CRITICAL: 'text-critical-700',
  HIGH: 'text-critical-700',
  MEDIUM: 'text-warn-700',
  LOW: 'text-zinc-600',
  INFO: 'text-zinc-500',
}

export function ReviewSection({ number, subject }: { number: number; subject: ResultSubject }) {
  const { data } = useReview(number)
  if (!data?.reviewed) return null

  const inScope = data.findings.filter((finding) => !finding.suppressed)
  const outOfScope = data.findings.filter((finding) => finding.suppressed)
  const attributes = data.attributes ?? {}
  const verdict = attributes.verdict

  return (
    <CollapsibleSection title="Revue IA" count={inScope.length} defaultOpen>
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
          {data.status && <StatusBadge style={status.pipeline(data.status)} />}
          {verdict && <span className="font-medium text-zinc-900">{VERDICT_LABEL[verdict] ?? verdict}</span>}
          {attributes.head && <span className="font-mono text-xs text-zinc-500">{attributes.head.slice(0, 7)}</span>}
          {attributes.reviewers && <span className="text-zinc-500">{attributes.reviewers} relecteur(s) ont répondu</span>}
          {attributes.failed && <span className="text-warn-700">sans réponse : {attributes.failed}</span>}
          {data.run_name && <RunLink runName={data.run_name} />}
          {data.reviewed_at && <span className="ml-auto text-zinc-500">{formatDateTime(data.reviewed_at)}</span>}
        </div>

        {data.summary && <p className="whitespace-pre-wrap text-sm text-zinc-700">{data.summary}</p>}
        {attributes.models && <p className="text-xs text-zinc-500">Modèles : {attributes.models}</p>}

        {inScope.length === 0 ? (
          <p className="text-sm text-zinc-500">Aucune remarque sur ce que la pull request change.</p>
        ) : (
          <ul className="flex flex-col">
            {inScope.map((finding) => (
              <ReviewFinding key={finding.fingerprint} finding={finding} />
            ))}
          </ul>
        )}

        {outOfScope.length > 0 && (
          <details className="text-sm">
            <summary className="cursor-pointer text-zinc-600">
              {outOfScope.length} remarque(s) hors du périmètre de la pull request
            </summary>
            <ul className="mt-2 flex flex-col opacity-70">
              {outOfScope.map((finding) => (
                <ReviewFinding key={finding.fingerprint} finding={finding} />
              ))}
            </ul>
          </details>
        )}

        <AttachmentList subject={subject} attachments={data.attachments} />
      </div>
    </CollapsibleSection>
  )
}

function ReviewFinding({ finding }: { finding: Finding }) {
  const [open, setOpen] = useState(false)

  return (
    <li className="border-t border-zinc-200 py-1.5 text-sm first:border-t-0">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className={`min-w-20 font-medium ${SEVERITY_TONE[finding.severity]}`}>
          {SEVERITY_LABEL[finding.severity] ?? finding.severity}
        </span>
        <span className="font-medium text-zinc-900">{finding.title}</span>
        {finding.location && <span className="font-mono text-xs text-zinc-600">{finding.location}</span>}
        {finding.rule_id && <span className="text-xs text-zinc-500">{finding.rule_id}</span>}
        <span className="ml-auto text-xs text-zinc-500">{finding.tool}</span>
        {finding.description && (
          <button
            type="button"
            onClick={() => setOpen((value) => !value)}
            aria-expanded={open}
            className="rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
          >
            {open ? 'Replier' : 'Détail'}
          </button>
        )}
      </div>
      {open && finding.description && (
        <p className="mt-2 whitespace-pre-wrap rounded-md border border-zinc-200 bg-zinc-50 p-2 text-xs text-zinc-700">
          {finding.description}
        </p>
      )}
    </li>
  )
}
