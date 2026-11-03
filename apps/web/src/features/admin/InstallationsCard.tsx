import { useState } from 'react'
import { Modal } from '../../components/Modal'
import { Card, EmptyState, ErrorState, Skeleton } from '../../components/primitives'
import { ApiError } from '../../lib/api'
import { formatDateTime } from '../../lib/format'
import {
  type Installation,
  type InstallMode,
  type PluginSource,
  useAcceptSource,
  useActivateInstallation,
  useCheckPlugin,
  useInstallations,
  useRemoveInstallation,
  useRequestInstallation,
  useSetPluginSource,
  useUpdatePlugin,
} from './installations'

/*
 * Installer un plugin depuis un registre OCI, et le tenir à jour.
 *
 * Par défaut on donne le dépôt : le plugin suit ses versions stables, le worker y
 * cherche régulièrement une version plus récente, et l'écran propose de mettre à
 * jour. La mise à jour remplace la version active, réglages et liaisons conservés.
 * Une étiquette ou un digest épingle le plugin : une version plus récente est
 * signalée, jamais proposée. Ce qui s'installe est toujours un digest, dont le
 * worker vérifie la signature contre les clés de l'exploitation.
 *
 * Une version installée à côté attend qu'on l'active. Retirer la version active
 * désinstalle le plugin, ses liaisons et ses réglages ; ses tables et les
 * résultats déjà lus restent, ses tables jusqu'à une purge.
 */

// What the dashboard records for a plugin the operator lets in without a trusted signature.
const UNSIGNED = 'unsigned'

const STATE_LABEL: Record<Installation['state'], string> = {
  pending: 'En cours',
  installed: 'Installé',
  failed: 'Échec',
  removed: 'Retiré',
  unrecognised: 'Source non reconnue',
}

function reason(error: Error | null): string | undefined {
  if (!error) return undefined
  return error instanceof ApiError && error.detail
    ? `${error.message} : ${error.detail}`
    : error.message
}

/** Un 409 à l'installation : le plugin est déjà installé depuis ce dépôt, et
 *  l'administrateur doit dire s'il installe à côté ou met à jour. */
function asksHow(error: Error | null): error is ApiError {
  return error instanceof ApiError && error.status === 409
}

export function InstallationsCard() {
  const { data, isPending, error, refetch } = useInstallations()
  const install = useRequestInstallation()
  const activate = useActivateInstallation()
  const remove = useRemoveInstallation()
  const accept = useAcceptSource()
  const update = useUpdatePlugin()
  const check = useCheckPlugin()
  const setSource = useSetPluginSource()
  const [reference, setReference] = useState('')
  const [choosing, setChoosing] = useState<{ reference: string; detail?: string } | null>(null)

  const failure =
    (asksHow(install.error) ? undefined : reason(install.error)) ??
    reason(activate.error) ??
    reason(remove.error) ??
    reason(accept.error) ??
    reason(update.error) ??
    reason(check.error) ??
    reason(setSource.error)
  const shown = data?.installations.filter((installation) => installation.state !== 'removed') ?? []
  const sources = new Map((data?.plugins ?? []).map((source) => [source.plugin, source]))

  const requestInstall = (wanted: string, mode?: InstallMode) =>
    install.mutate(
      { reference: wanted, mode },
      {
        onSuccess: () => {
          setReference('')
          setChoosing(null)
        },
        onError: (failed) => {
          if (asksHow(failed)) setChoosing({ reference: wanted, detail: failed.detail })
        },
      },
    )

  return (
    <Card title="Plugins installés">
      <form
        className="mb-4 flex flex-col gap-2"
        onSubmit={(event) => {
          event.preventDefault()
          requestInstall(reference.trim())
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
            placeholder="registre.example/plugins/gotest"
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
          Le dépôt seul : le plugin suit ses versions stables, et le dashboard propose ses mises à
          jour. Une étiquette (<span className="font-mono">:2.4.0</span>) ou un digest
          (<span className="font-mono">@sha256:…</span>) l'épingle. Le plugin doit être signé par
          l'une des clés de confiance de l'exploitation.
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
                  {installation.state === 'unrecognised' && (
                    <button
                      type="button"
                      disabled={accept.isPending}
                      onClick={() => {
                        if (
                          window.confirm(
                            `Ce plugin ne vient pas d'une source reconnue par cette instance. ` +
                              `L'accepter retient ${installation.plugin} : ses prochaines versions ` +
                              `s'installeront sans reposer la question.`,
                          )
                        )
                          accept.mutate(installation.id)
                      }}
                      className="rounded-md border border-warn-700 bg-white px-2.5 py-1 text-xs font-medium text-warn-700 transition-colors hover:bg-warn-50"
                    >
                      Accepter la source
                    </button>
                  )}
                  {installation.state !== 'pending' && (
                    <button
                      type="button"
                      disabled={remove.isPending}
                      onClick={() => {
                        const question = installation.active
                          ? `Désinstaller ${installation.plugin} ? Ses définitions, liaisons et réglages seront retirés ; ses tables et les résultats déjà lus restent, ses tables jusqu'à une purge.`
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
              {installation.active && installation.plugin && sources.get(installation.plugin) && (
                <SourceLine
                  source={sources.get(installation.plugin) as PluginSource}
                  busy={update.isPending || check.isPending || setSource.isPending}
                  onUpdate={(source) => {
                    const warning = source.major
                      ? ` C'est une version majeure : elle peut changer ce que le plugin fait ou demande.`
                      : ''
                    if (
                      window.confirm(
                        `Mettre à jour ${source.plugin} de ${source.version} vers ${source.latest_version} ? ` +
                          `La version ${source.version} sera retirée ; les réglages et les liaisons du plugin sont conservés.` +
                          warning,
                      )
                    )
                      update.mutate(source.plugin)
                  }}
                  onCheck={(source) => check.mutate(source.plugin)}
                  onPin={(source, pinned) => setSource.mutate({ plugin: source.plugin, pinned })}
                />
              )}
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
              {installation.state === 'unrecognised' ? (
                <span className="text-xs text-warn-700">
                  Attention, ce plugin ne vient pas d'une source reconnue
                  {installation.error && ` : ${installation.error}`}. L'installer est votre
                  décision ; elle sera retenue pour les prochaines versions de ce plugin.
                </span>
              ) : (
                installation.error && (
                  <span className="text-xs text-critical-700">{installation.error}</span>
                )
              )}
              <span className="text-xs text-zinc-400">demandé par {installation.requested_by}</span>
            </li>
          ))}
        </ul>
      )}

      {failure && <p className="mt-3 text-sm text-critical-700">{failure}</p>}

      {choosing && (
        <Modal title="Plugin déjà installé" onClose={() => setChoosing(null)}>
          <div className="flex flex-col gap-3 text-sm text-zinc-700">
            <p>{choosing.detail ?? 'Ce plugin est déjà installé depuis ce dépôt.'}</p>
            <p>
              Mettre à jour remplace la version active, réglages et liaisons conservés. Installer à
              côté garde la version active : la nouvelle attendra qu'on l'active.
            </p>
            <div className="flex justify-end gap-2">
              <button
                type="button"
                onClick={() => setChoosing(null)}
                className="rounded-md border border-zinc-300 px-3 py-1.5 text-sm"
              >
                Annuler
              </button>
              <button
                type="button"
                disabled={install.isPending}
                onClick={() => requestInstall(choosing.reference, 'install')}
                className="rounded-md border border-zinc-300 bg-white px-3 py-1.5 text-sm font-medium text-zinc-700 hover:bg-zinc-100"
              >
                Installer à côté
              </button>
              <button
                type="button"
                disabled={install.isPending}
                onClick={() => requestInstall(choosing.reference, 'update')}
                className="rounded-md border border-zinc-900 bg-zinc-900 px-3 py-1.5 text-sm font-medium text-white hover:bg-zinc-700"
              >
                Mettre à jour l'existant
              </button>
            </div>
          </div>
        </Modal>
      )}
    </Card>
  )
}

/**
 * Ce que la version active suit, et ce que son dépôt propose.
 *
 * Un plugin épinglé dit qu'il est en retard, sans bouton de mise à jour : c'est
 * l'épinglage que l'administrateur a choisi. Le suivre de nouveau fera apparaître
 * le bouton.
 */
function SourceLine({
  source,
  busy,
  onUpdate,
  onCheck,
  onPin,
}: {
  source: PluginSource
  busy: boolean
  onUpdate: (source: PluginSource) => void
  onCheck: (source: PluginSource) => void
  onPin: (source: PluginSource, pinned: boolean) => void
}) {
  const link = 'text-xs font-medium text-zinc-700 underline-offset-2 hover:underline disabled:opacity-40'
  return (
    <div className="mt-1 flex flex-col gap-1 rounded-md bg-zinc-50 px-2 py-1.5">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 text-xs text-zinc-600">
        <span className="rounded border border-zinc-300 px-1.5 py-0.5">
          {source.pinned ? 'Épinglé' : 'Suit le dépôt'}
        </span>
        <span className="break-all font-mono">{source.repository}</span>
        <span className="text-zinc-400">
          {source.check_requested
            ? 'vérification en cours'
            : source.checked_at
              ? `vérifié le ${formatDateTime(source.checked_at)}`
              : 'pas encore vérifié'}
        </span>
        <span className="ml-auto flex gap-3">
          <button type="button" disabled={busy || source.check_requested} onClick={() => onCheck(source)} className={link}>
            Vérifier maintenant
          </button>
          <button type="button" disabled={busy} onClick={() => onPin(source, !source.pinned)} className={link}>
            {source.pinned ? 'Suivre le dépôt' : 'Épingler cette version'}
          </button>
        </span>
      </div>
      {source.check_error && (
        <span className="text-xs text-critical-700">Le dépôt n'a pas pu être lu : {source.check_error}</span>
      )}
      {source.update_available && !source.pinned && (
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="font-medium text-zinc-900">Version {source.latest_version} disponible</span>
          {source.major && <span className="text-xs text-warn-700">version majeure</span>}
          <button
            type="button"
            disabled={busy}
            onClick={() => onUpdate(source)}
            className="ml-auto rounded-md border border-zinc-900 bg-zinc-900 px-2.5 py-1 text-xs font-medium text-white transition-colors hover:bg-zinc-700 disabled:opacity-40"
          >
            Mettre à jour
          </button>
        </div>
      )}
      {source.update_available && source.pinned && (
        <span className="text-xs text-warn-700">
          Version {source.latest_version} disponible. Le plugin est épinglé à {source.version} : il
          ne se met pas à jour depuis l'écran.
        </span>
      )}
    </div>
  )
}
