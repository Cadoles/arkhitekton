import { useState } from 'react'
import { Card, EmptyState, ErrorState, Skeleton } from '../../components/primitives'
import { ApiError } from '../../lib/api'
import {
  type Binding,
  CLUSTER_DEPLOY,
  type Capability,
  useBindCapability,
  useCapabilities,
  useUnbindCapability,
} from './capabilities'

/*
 * Lier les capacités de pipeline : quels secrets satisfont leurs besoins,
 * quelles clés de configuration remplissent leurs paramètres, comment leurs
 * volumes sont dimensionnés, combien de temps et combien à la fois.
 *
 * L'écran désigne des secrets, il ne les lit jamais (doc 07 §7.3). Il ne nomme
 * pas non plus de compte de service : une capacité qui déploie se lie dans le
 * fichier de catalogue, avec le déploiement.
 */

export function CapabilitiesScreen() {
  const { data, isPending, error, refetch } = useCapabilities()

  if (isPending) return <Skeleton className="h-64 w-full" />
  if (error) {
    return (
      <ErrorState
        message="Les capacités n'ont pas pu être chargées."
        onRetry={() => void refetch()}
      />
    )
  }
  if (!data) return null

  return (
    <Card title={`Capacités de pipeline (${data.capabilities.length})`}>
      <p className="mb-3 text-sm text-zinc-500">
        Une capacité se lance une fois liée. Ce que lie le fichier de catalogue l'emporte et se
        modifie avec le déploiement ; le reste se lie ici.
      </p>
      {data.capabilities.length === 0 ? (
        <EmptyState>Aucune capacité de pipeline sur cette instance.</EmptyState>
      ) : (
        <ul className="flex flex-col gap-3">
          {data.capabilities.map((capability) => (
            <CapabilityCard
              // La liaison enregistrée entre dans la clé : sans cela, la carte
              // garderait le brouillon que le serveur vient de remplacer.
              key={`${capability.ref}:${JSON.stringify(capability.binding ?? null)}`}
              capability={capability}
            />
          ))}
        </ul>
      )}
    </Card>
  )
}

/** Ce que l'on saisit, champ par champ, avant d'en faire une liaison. */
export type BindingDraft = {
  timeout: string
  maxConcurrent: string
  params: Record<string, string>
  /** Par besoin : un Secret nommé, ou une clé de configuration qui le nomme. */
  needs: Record<string, { source: 'secretName' | 'configKey'; value: string }>
  volumes: Record<string, { storage: string; storageClassName: string }>
  /** Par espace de travail : une ConfigMap nommée, ou une clé de configuration qui la nomme. */
  configMaps: Record<string, { source: 'name' | 'configKey'; value: string }>
}

export function draftOf(capability: Capability): BindingDraft {
  const binding = capability.binding
  const needs: BindingDraft['needs'] = {}
  for (const need of capability.needs) {
    const secret = binding?.needs?.[need.name]?.secret
    needs[need.name] = secret?.configKey
      ? { source: 'configKey', value: secret.configKey }
      : { source: 'secretName', value: secret?.secretName ?? '' }
  }
  return {
    timeout: binding?.timeout ?? '',
    maxConcurrent: binding?.maxConcurrent ? String(binding.maxConcurrent) : '',
    params: Object.fromEntries(
      capability.params.map((name) => [name, binding?.params?.[name]?.key ?? '']),
    ),
    needs,
    volumes: Object.fromEntries(
      capability.volumes.map((name) => [
        name,
        {
          storage: binding?.volumes?.[name]?.storage ?? '',
          storageClassName: binding?.volumes?.[name]?.storageClassName ?? '',
        },
      ]),
    ),
    configMaps: Object.fromEntries(
      capability.config_maps.map(({ workspace }) => {
        const bound = binding?.configMaps?.[workspace]
        return [
          workspace,
          bound?.configKey
            ? { source: 'configKey' as const, value: bound.configKey }
            : { source: 'name' as const, value: bound?.name ?? '' },
        ]
      }),
    ),
  }
}

/** La liaison que le brouillon décrit. Un champ vide n'est pas envoyé : il laisse
 *  la capacité à son défaut plutôt que d'écrire une valeur vide. */
export function bindingOf(ref: string, draft: BindingDraft): Binding {
  const binding: Binding = { ref }
  if (draft.timeout.trim()) binding.timeout = draft.timeout.trim()
  const concurrent = Number.parseInt(draft.maxConcurrent, 10)
  if (concurrent > 0) binding.maxConcurrent = concurrent

  for (const [name, key] of Object.entries(draft.params)) {
    if (key.trim()) (binding.params ??= {})[name] = { key: key.trim() }
  }
  for (const [name, need] of Object.entries(draft.needs)) {
    if (name === CLUSTER_DEPLOY || !need.value.trim()) continue
    ;(binding.needs ??= {})[name] = {
      secret: { [need.source]: need.value.trim() },
    }
  }
  for (const [name, volume] of Object.entries(draft.volumes)) {
    const sized: { storage?: string; storageClassName?: string } = {}
    if (volume.storage.trim()) sized.storage = volume.storage.trim()
    if (volume.storageClassName.trim()) sized.storageClassName = volume.storageClassName.trim()
    if (Object.keys(sized).length > 0) (binding.volumes ??= {})[name] = sized
  }
  for (const [workspace, bound] of Object.entries(draft.configMaps)) {
    if (!bound.value.trim()) continue
    ;(binding.configMaps ??= {})[workspace] = { [bound.source]: bound.value.trim() }
  }
  return binding
}

const input = 'min-w-0 rounded-md border border-zinc-300 px-2 py-1 font-mono text-sm'

export function CapabilityCard({ capability }: { capability: Capability }) {
  const bind = useBindCapability()
  const unbind = useUnbindCapability()
  const [draft, setDraft] = useState(() => draftOf(capability))
  const byFile = capability.bound_by === 'file'
  const deploys = capability.needs.some((need) => need.name === CLUSTER_DEPLOY)
  const failure = bind.error ?? unbind.error

  return (
    <li className="flex flex-col gap-3 rounded-md border border-zinc-200 px-3 py-3">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <span className="font-medium text-zinc-900">{capability.label}</span>
        <span className="font-mono text-xs text-zinc-500">{capability.ref}</span>
        <span className="ml-auto text-xs text-zinc-500">
          {byFile
            ? 'Liée par le fichier'
            : capability.bound_by === 'database'
              ? 'Liée ici'
              : 'Non liée'}
        </span>
      </div>

      {capability.unavailable && (
        <p className="text-xs text-warn-700">Indisponible : {capability.unavailable}</p>
      )}

      {byFile ? (
        <p className="text-sm text-zinc-500">
          Le fichier de catalogue lie cette capacité : sa liaison se modifie avec le déploiement.
        </p>
      ) : (
        <>
          {deploys && (
            <p className="text-xs text-warn-700">
              Elle demande <span className="font-mono">{CLUSTER_DEPLOY}</span>, un compte de service
              : elle ne peut être liée que par le fichier de catalogue.
            </p>
          )}

          {capability.needs
            .filter((need) => need.name !== CLUSTER_DEPLOY)
            .map((need) => {
              const value = draft.needs[need.name] ?? {
                source: 'secretName' as const,
                value: '',
              }
              return (
                <div key={need.name} className="flex flex-col gap-1">
                  <label
                    htmlFor={`${capability.ref}-need-${need.name}`}
                    className="text-sm text-zinc-700"
                  >
                    Besoin <span className="font-mono">{need.name}</span>
                    {need.reason && <span className="text-zinc-500"> : {need.reason}</span>}
                  </label>
                  <div className="flex flex-wrap gap-2">
                    <select
                      aria-label={`Source du secret de ${need.name}`}
                      value={value.source}
                      onChange={(event) =>
                        setDraft((current) => ({
                          ...current,
                          needs: {
                            ...current.needs,
                            [need.name]: {
                              ...value,
                              source: event.target.value as 'secretName' | 'configKey',
                            },
                          },
                        }))
                      }
                      className="rounded-md border border-zinc-300 px-2 py-1 text-sm"
                    >
                      <option value="secretName">Secret nommé</option>
                      <option value="configKey">Clé de configuration</option>
                    </select>
                    <input
                      id={`${capability.ref}-need-${need.name}`}
                      value={value.value}
                      placeholder={
                        value.source === 'secretName' ? 'git-credentials' : 'pipeline.gitSecret'
                      }
                      onChange={(event) =>
                        setDraft((current) => ({
                          ...current,
                          needs: {
                            ...current.needs,
                            [need.name]: {
                              ...value,
                              value: event.target.value,
                            },
                          },
                        }))
                      }
                      className={`flex-1 ${input}`}
                    />
                  </div>
                </div>
              )
            })}

          {capability.params.map((name) => (
            <div key={name} className="flex flex-col gap-1">
              <label htmlFor={`${capability.ref}-param-${name}`} className="text-sm text-zinc-700">
                Paramètre <span className="font-mono">{name}</span>, lu depuis la clé
              </label>
              <input
                id={`${capability.ref}-param-${name}`}
                value={draft.params[name] ?? ''}
                placeholder="clé par défaut de la capacité"
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    params: { ...current.params, [name]: event.target.value },
                  }))
                }
                className={input}
              />
            </div>
          ))}

          {capability.volumes.map((name) => {
            const volume = draft.volumes[name] ?? {
              storage: '',
              storageClassName: '',
            }
            const set = (field: 'storage' | 'storageClassName', value: string) =>
              setDraft((current) => ({
                ...current,
                volumes: {
                  ...current.volumes,
                  [name]: { ...volume, [field]: value },
                },
              }))
            return (
              <div key={name} className="flex flex-wrap items-center gap-2 text-sm text-zinc-700">
                <span>
                  Volume <span className="font-mono">{name}</span>
                </span>
                <input
                  aria-label={`Taille du volume ${name}`}
                  value={volume.storage}
                  placeholder="1Gi"
                  onChange={(event) => set('storage', event.target.value)}
                  className={`w-24 ${input}`}
                />
                <input
                  aria-label={`Classe de stockage du volume ${name}`}
                  value={volume.storageClassName}
                  placeholder="classe par défaut"
                  onChange={(event) => set('storageClassName', event.target.value)}
                  className={input}
                />
              </div>
            )
          })}

          {capability.config_maps.map(({ workspace, reason }) => {
            const bound = draft.configMaps[workspace] ?? {
              source: 'name' as const,
              value: '',
            }
            return (
              <div key={workspace} className="flex flex-col gap-1">
                <label
                  htmlFor={`${capability.ref}-configmap-${workspace}`}
                  className="text-sm text-zinc-700"
                >
                  ConfigMap de <span className="font-mono">{workspace}</span>
                  {reason && <span className="text-zinc-500"> : {reason}</span>}
                </label>
                <div className="flex flex-wrap gap-2">
                  <select
                    aria-label={`Source de la ConfigMap de ${workspace}`}
                    value={bound.source}
                    onChange={(event) =>
                      setDraft((current) => ({
                        ...current,
                        configMaps: {
                          ...current.configMaps,
                          [workspace]: {
                            ...bound,
                            source: event.target.value as 'name' | 'configKey',
                          },
                        },
                      }))
                    }
                    className="rounded-md border border-zinc-300 px-2 py-1 text-sm"
                  >
                    <option value="name">ConfigMap nommée</option>
                    <option value="configKey">Clé de configuration</option>
                  </select>
                  <input
                    id={`${capability.ref}-configmap-${workspace}`}
                    value={bound.value}
                    placeholder={
                      bound.source === 'name' ? 'security-policies' : 'security.policies'
                    }
                    onChange={(event) =>
                      setDraft((current) => ({
                        ...current,
                        configMaps: {
                          ...current.configMaps,
                          [workspace]: { ...bound, value: event.target.value },
                        },
                      }))
                    }
                    className={`flex-1 ${input}`}
                  />
                </div>
              </div>
            )
          })}

          <div className="flex flex-wrap items-center gap-2 text-sm text-zinc-700">
            <label htmlFor={`${capability.ref}-timeout`}>Durée maximale</label>
            <input
              id={`${capability.ref}-timeout`}
              value={draft.timeout}
              placeholder="45m"
              onChange={(event) =>
                setDraft((current) => ({
                  ...current,
                  timeout: event.target.value,
                }))
              }
              className={`w-24 ${input}`}
            />
            <label htmlFor={`${capability.ref}-concurrent`}>Runs simultanés</label>
            <input
              id={`${capability.ref}-concurrent`}
              type="number"
              min={1}
              value={draft.maxConcurrent}
              onChange={(event) =>
                setDraft((current) => ({
                  ...current,
                  maxConcurrent: event.target.value,
                }))
              }
              className={`w-20 ${input}`}
            />
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              disabled={bind.isPending}
              onClick={() => bind.mutate(bindingOf(capability.ref, draft))}
              className="rounded-md border border-zinc-900 bg-zinc-900 px-2.5 py-1 text-xs font-medium text-white transition-colors hover:bg-zinc-700 disabled:opacity-40"
            >
              Enregistrer la liaison
            </button>
            {capability.bound_by === 'database' && (
              <button
                type="button"
                disabled={unbind.isPending}
                onClick={() => unbind.mutate(capability.ref)}
                className="rounded-md border border-zinc-300 bg-white px-2.5 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
              >
                Délier
              </button>
            )}
          </div>
        </>
      )}

      {failure && (
        <p className="text-sm text-critical-700">
          {failure instanceof ApiError && failure.detail
            ? `${failure.message} : ${failure.detail}`
            : failure.message}
        </p>
      )}
    </li>
  )
}
