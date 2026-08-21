import { useState } from 'react'
import { Icon } from '../../components/Icon'
import { Card, EmptyState, ErrorState, Skeleton, StatusBadge } from '../../components/primitives'
import { status } from '../../lib/status'
import { PipelineTimeline } from './PipelineTimeline'
import { RunLink } from './RunLink'
import { SEVERITY_LABEL, useSecurity, type Finding, type SecurityReport, type Severity } from './tests'

/*
 * Findings de sécurité d'une release.
 *
 * Le bloc porte une distinction que les compteurs seuls ne peuvent pas rendre :
 * un audit dont un scanner a échoué produit zéro vulnérabilité, exactement comme
 * un audit propre. Le bandeau d'incomplétude passe donc **avant** les chiffres —
 * les lire sans lui reviendrait à conclure « rien trouvé » là où personne n'a
 * regardé.
 *
 * Sans cadre, comme `FamilyDetail` : il s'affiche dans la modale ouverte depuis
 * la tuile Sécurité du score.
 */
export function SecurityDetail({ tag, runName }: { tag: string | undefined; runName?: string }) {
  const { data, isPending, error, refetch } = useSecurity(tag)
  const [showSuppressed, setShowSuppressed] = useState(false)

  return (
    <div className="flex flex-col gap-3">
      {/* La frise avant tout le reste : quand l'audit tourne, ce qui suit décrit
          le précédent. */}
      {runName && <PipelineTimeline runName={runName} />}

      {isPending && <Skeleton className="h-64 w-full" />}
      {error && <ErrorState message="L'audit n'a pas pu être chargé." onRetry={() => void refetch()} />}

      {data && !data.audited && (
        // Distinct d'un audit vide : le dire plutôt que d'afficher des zéros,
        // qui se liraient comme « rien trouvé ».
        <EmptyState>Aucun audit de sécurité n'a été exécuté pour cette release.</EmptyState>
      )}

      {data?.audited && (
        <>
          {data.run_name && (
            <div className="flex flex-wrap items-center gap-3 text-xs text-zinc-500">
              <RunLink runName={data.run_name} />
            </div>
          )}
          <Overview report={data} />
          <FindingsCard
            findings={data.findings ?? []}
            showSuppressed={showSuppressed}
            onToggle={() => setShowSuppressed((value) => !value)}
          />
        </>
      )}
    </div>
  )
}

const SEVERITY_TONE: Record<Severity, string> = {
  CRITICAL: 'text-critical-700',
  HIGH: 'text-critical-700',
  MEDIUM: 'text-warn-700',
  LOW: 'text-zinc-600',
  INFO: 'text-zinc-500',
}

function Overview({ report }: { report: SecurityReport }) {
  const counts = report.counts
  const delta = report.delta

  return (
    <Card
      title="Verdict"
      action={
        report.generated_at ? <span className="text-xs text-zinc-500">{report.generated_at}</span> : undefined
      }
    >
      {report.measured === false && (
        <p className="mb-3 flex items-start gap-2 rounded-md border border-warn-500 bg-warn-50 px-3 py-2 text-sm text-warn-700">
          <Icon name="alert" className="mt-0.5 size-4 shrink-0" />
          <span>
            Audit incomplet : {report.failed_scanners?.join(', ')} n'a pas conclu. Les compteurs ci-dessous ne portent
            que sur ce qui a pu être analysé, et la sécurité ne note pas le score de cette release — son poids est
            redistribué sur les autres signaux.
          </span>
        </p>
      )}

      {counts && (
        <ul className="flex flex-wrap gap-4 text-sm">
          {(['CRITICAL', 'HIGH', 'MEDIUM', 'LOW'] as const).map((severity) => {
            const value = { CRITICAL: counts.critical, HIGH: counts.high, MEDIUM: counts.medium, LOW: counts.low }[
              severity
            ]
            return (
              <li key={severity} className="flex items-baseline gap-2">
                <span className={`text-xl font-semibold ${SEVERITY_TONE[severity]}`}>{value}</span>
                <span className="text-zinc-500">{SEVERITY_LABEL[severity]}</span>
              </li>
            )
          })}
          {counts.suppressed > 0 && (
            <li className="flex items-baseline gap-2">
              <span className="text-xl font-semibold text-zinc-500">{counts.suppressed}</span>
              <span className="text-zinc-500">écarté(s) par exception</span>
            </li>
          )}
        </ul>
      )}

      {delta && (report.previous_run_id || delta.new > 0 || delta.resolved > 0) && (
        // La comparaison au run précédent : c'est elle qui distingue une
        // régression d'une dette connue.
        <p className="mt-3 border-t border-zinc-200 pt-3 text-sm text-zinc-600">
          Depuis l'audit précédent : <span className="text-critical-700">{delta.new} nouveau(x)</span>,{' '}
          {delta.existing} inchangé(s), <span className="text-healthy-700">{delta.resolved} résolu(s)</span>.
        </p>
      )}

      {report.policy?.fail_on && (
        <p className="mt-2 text-sm text-zinc-500">
          Politique appliquée : échec à partir de {SEVERITY_LABEL[report.policy.fail_on as Severity] ?? report.policy.fail_on}.
        </p>
      )}
    </Card>
  )
}

function FindingsCard({
  findings,
  showSuppressed,
  onToggle,
}: {
  findings: Finding[]
  showSuppressed: boolean
  onToggle: () => void
}) {
  const suppressed = findings.filter((finding) => finding.suppressed)
  const shown = showSuppressed ? findings : findings.filter((finding) => !finding.suppressed)

  return (
    <Card
      title={`Findings (${shown.length})`}
      action={
        suppressed.length > 0 ? (
          <button
            type="button"
            onClick={onToggle}
            aria-pressed={showSuppressed}
            className="rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
          >
            {showSuppressed ? 'Masquer' : 'Afficher'} les {suppressed.length} écarté(s)
          </button>
        ) : undefined
      }
    >
      {shown.length === 0 ? (
        <EmptyState>Aucun finding. 🎉</EmptyState>
      ) : (
        <ul className="flex flex-col">
          {shown.map((finding) => (
            <FindingRow key={finding.fingerprint} finding={finding} />
          ))}
        </ul>
      )}
    </Card>
  )
}

function FindingRow({ finding }: { finding: Finding }) {
  const [open, setOpen] = useState(false)

  return (
    <li className={`border-t border-zinc-200 py-1.5 text-sm first:border-t-0 ${finding.suppressed ? 'opacity-60' : ''}`}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className={`min-w-20 font-medium ${SEVERITY_TONE[finding.severity]}`}>
          {SEVERITY_LABEL[finding.severity]}
        </span>
        <span className="font-medium text-zinc-900">{finding.title}</span>
        <span className="font-mono text-xs text-zinc-500">{finding.rule_id}</span>

        {/* Nouveau ou dette connue : la distinction décide de la réaction. */}
        {finding.status === 'new' && !finding.suppressed && (
          <StatusBadge style={status.pipeline('fail')} />
        )}

        {finding.package && (
          <span className="text-zinc-600">
            {finding.package}
            {finding.installed && ` ${finding.installed}`}
            {/* La version qui corrige, à côté de celle en place : c'est l'écart
                qui décide d'une action. */}
            {finding.fixed && <span className="text-healthy-700"> → {finding.fixed}</span>}
          </span>
        )}
        {finding.location && <span className="font-mono text-xs text-zinc-500">{finding.location}</span>}

        <span className="ml-auto text-xs text-zinc-500">{finding.tool}</span>
        {(finding.description || finding.suppression_reason) && (
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

      {open && (
        <div className="mt-2 rounded-md border border-zinc-200 bg-zinc-50 p-2 text-xs text-zinc-700">
          {finding.description && <p className="whitespace-pre-wrap">{finding.description}</p>}
          {finding.suppression_reason && (
            // Une exception est une décision humaine datée : son motif est
            // affiché, sans quoi elle serait indéfendable en revue.
            <p className="mt-2 text-zinc-600">
              Écarté par exception : {finding.suppression_reason}
              {finding.suppression_expires && ` — expire le ${finding.suppression_expires}`}
            </p>
          )}
        </div>
      )}
    </li>
  )
}
