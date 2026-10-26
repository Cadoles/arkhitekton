import { useState } from 'react'
import { Card, EmptyState, ErrorState, Skeleton } from '../../components/primitives'
import { ApiError } from '../../lib/api'
import {
  type Installation,
  useActivateInstallation,
  useInstallations,
  useRemoveInstallation,
  useRequestInstallation,
} from './installations'

/*
 * Installer un plugin depuis un registre OCI.
 *
 * Par digest seulement : une étiquette se déplace, et ce que l'administrateur a
 * examiné doit être ce qui s'installe. Le worker vérifie la signature contre les
 * clés que l'exploitation a montées ; l'écran n'en choisit aucune.
 *
 * Deux versions d'un plugin cohabitent : la nouvelle attend qu'on l'active, et
 * l'ancienne reste chargée pour lire les runs qu'elle a lancés. Retirer la version
 * active désinstalle le plugin, ses liaisons et ses réglages ; les résultats déjà
 * lus restent.
 */

// What the dashboard records for a plugin the operator lets in without a trusted signature.
const UNSIGNED = 'unsigned'

const STATE_LABEL: Record<Installation['state'], string> = {
  pending: 'En cours',
  installed: 'Installé',
  failed: 'Échec',
  removed: 'Retiré',
}

function reason(error: Error | null): string | undefined {
  if (!error) return undefined
  return error instanceof ApiError && error.detail
    ? `${error.message} : ${error.detail}`
    : error.message
}

export function InstallationsCard() {
  const { data, isPending, error, refetch } = useInstallations()
  const install = useRequestInstallation()
  const activate = useActivateInstallation()
  const remove = useRemoveInstallation()
  const [reference, setReference] = useState('')

  const failure = reason(install.error) ?? reason(activate.error) ?? reason(remove.error)
  const shown = data?.installations.filter((installation) => installation.state !== 'removed') ?? []

  return (
    <Card title="Plugins installés">
      <form
        className="mb-4 flex flex-col gap-2"
        onSubmit={(event) => {
          event.preventDefault()
          install.mutate(reference.trim(), {
            onSuccess: () => setReference(''),
          })
        }}
      >
        <label htmlFor="plugin-reference" className="text-sm font-medium text-zinc-700">
          Référence du plugin
        </label>
        <div className="flex flex-wrap gap-2">
          <input
            id="plugin-reference"
            value={reference}
            onChange={(event) => setReference(event.target.value)}
            placeholder="registre.example/plugins/gotest@sha256:…"
            className="min-w-0 flex-1 rounded-md border border-zinc-300 px-2 py-1 font-mono text-sm"
          />
          <button
            type="submit"
            disabled={install.isPending || reference.trim() === ''}
            className="rounded-md border border-zinc-900 bg-zinc-900 px-2.5 py-1 text-xs font-medium text-white transition-colors hover:bg-zinc-700 disabled:opacity-40"
          >
            Installer
          </button>
        </div>
        <p className="text-xs text-zinc-500">
          Épinglée par digest. Le plugin doit être signé par l'une des clés de confiance de
          l'exploitation.
        </p>
      </form>

      {isPending ? (
        <Skeleton className="h-24 w-full" />
      ) : error ? (
        <ErrorState
          message="Les installations n'ont pas pu être chargées."
          onRetry={() => void refetch()}
        />
      ) : shown.length === 0 ? (
        <EmptyState>Aucun plugin installé depuis un registre.</EmptyState>
      ) : (
        <ul className="flex flex-col gap-2">
          {shown.map((installation) => (
            <li
              key={installation.id}
              className="flex flex-col gap-1 rounded-md border border-zinc-200 px-3 py-2 text-sm"
            >
              <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
                <span className="font-medium text-zinc-900">
                  {installation.plugin
                    ? `${installation.plugin} ${installation.version}`
                    : 'Plugin en attente'}
                </span>
                <span className="rounded border border-zinc-200 px-1.5 py-0.5 text-xs text-zinc-500">
                  {STATE_LABEL[installation.state]}
                </span>
                {installation.active && (
                  <span className="rounded border border-zinc-900 px-1.5 py-0.5 text-xs text-zinc-900">
                    Active
                  </span>
                )}
                <span className="ml-auto flex gap-2">
                  {installation.state === 'installed' && !installation.active && (
                    <button
                      type="button"
                      disabled={activate.isPending}
                      onClick={() => activate.mutate(installation.id)}
                      className="rounded-md border border-zinc-300 bg-white px-2.5 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
                    >
                      Activer
                    </button>
                  )}
                  {installation.state !== 'pending' && (
                    <button
                      type="button"
                      disabled={remove.isPending}
                      onClick={() => {
                        const question = installation.active
                          ? `Désinstaller ${installation.plugin} ? Ses définitions, liaisons et réglages seront retirés ; les résultats déjà lus restent.`
                          : 'Retirer cette version ?'
                        if (window.confirm(question)) remove.mutate(installation.id)
                      }}
                      className="rounded-md border border-zinc-300 bg-white px-2.5 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
                    >
                      {installation.active ? 'Désinstaller' : 'Retirer'}
                    </button>
                  )}
                </span>
              </div>
              <span className="break-all font-mono text-xs text-zinc-500">
                {installation.reference}
              </span>
              {installation.signed_by === UNSIGNED ? (
                <span className="text-xs text-warn-700">
                  Non signé : installé parce que l'exploitation l'autorise nommément
                </span>
              ) : (
                installation.signed_by && (
                  <span className="break-all text-xs text-zinc-500">
                    Signé par <span className="font-mono">{installation.signed_by}</span>
                  </span>
                )
              )}
              {installation.error && (
                <span className="text-xs text-critical-700">{installation.error}</span>
              )}
              <span className="text-xs text-zinc-400">demandé par {installation.requested_by}</span>
            </li>
          ))}
        </ul>
      )}

      {failure && <p className="mt-3 text-sm text-critical-700">{failure}</p>}
    </Card>
  )
}
