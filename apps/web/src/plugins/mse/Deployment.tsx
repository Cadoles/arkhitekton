import { CollapsibleSection, ConfirmDialog, EmptyState, formatDateTime, type ReleaseDetail } from '@arkhitekton/host'
import { useState } from 'react'
import { useEnvironments, usePromote, useReleaseTargets, type PromotionTarget } from './api'

/**
 * La section Déploiement de l'écran d'une release : où elle peut aller, où elle
 * a été promue, et le geste de promotion.
 */
export function Deployment({ release }: { release: ReleaseDetail }) {
  const environments = useEnvironments()
  const promote = usePromote()
  const [target, setTarget] = useState<PromotionTarget | null>(null)
  const targets = useReleaseTargets(release.tag).data?.targets ?? []

  const promoted = new Map((release.promotions ?? []).map((promotion) => [promotion.environment, promotion]))
  const sensitive = new Set(
    (environments.data?.environments ?? []).filter((entry) => entry.sensitive).map((entry) => entry.id),
  )

  return (
    <CollapsibleSection title="Déploiement" count={targets.length}>
      {targets.length === 0 ? (
        <EmptyState>Aucun environnement déclaré.</EmptyState>
      ) : (
        <ul className="flex flex-col gap-2">
          {targets.map((entry) => {
            const done = promoted.get(entry.environment)
            return (
              <li
                key={entry.environment}
                className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border border-zinc-200 px-3 py-2 text-sm"
              >
                <span className="font-medium text-zinc-900">{entry.label}</span>

                {done ? (
                  <span className="text-zinc-500">
                    promue <span className="font-mono">{done.tag}</span> — {formatDateTime(done.pushed_at)}
                  </span>
                ) : entry.allowed ? (
                  <span className="font-mono text-zinc-500">{entry.tag}</span>
                ) : (
                  // Le motif accompagne toujours le refus : un bouton grisé sans
                  // explication se lit comme une panne.
                  <span className="text-zinc-500">{entry.reason}</span>
                )}

                {entry.allowed && (
                  <button
                    type="button"
                    onClick={() => setTarget(entry)}
                    className="ml-auto rounded-md border border-zinc-300 bg-white px-3 py-1 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
                  >
                    {done ? 'Repromouvoir' : 'Promouvoir'}
                  </button>
                )}
              </li>
            )
          })}
        </ul>
      )}

      {promote.isError && <p className="mt-3 text-sm text-critical-700">{(promote.error as Error).message}</p>}

      {target && (
        <ConfirmDialog
          title="Promouvoir cette release ?"
          confirmLabel="Promouvoir"
          pending={promote.isPending}
          challenge={sensitive.has(target.environment) ? release.tag : undefined}
          onCancel={() => setTarget(null)}
          onConfirm={() => {
            promote.mutate({ tag: release.tag, environment: target.environment })
            setTarget(null)
          }}
        >
          <p>
            Release <span className="font-mono font-medium text-zinc-900">{release.tag}</span>
          </p>
          <p>
            Environnement <span className="font-medium text-zinc-900">{target.label}</span>
          </p>
          <p className="text-zinc-500">
            Le tag <span className="font-mono">{target.tag}</span> sera poussé dans le registre. Le déploiement suivra
            la réconciliation de Flux.
          </p>
        </ConfirmDialog>
      )}
    </CollapsibleSection>
  )
}
