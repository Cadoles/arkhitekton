import { useState, type ReactNode } from 'react'
import { Icon } from '../../components/Icon'
import { Modal } from '../../components/Modal'
import { Card, EmptyState, ErrorState, Skeleton } from '../../components/primitives'
import { Select } from '../../components/Select'
import { ApiError } from '../../lib/api'
import { useQueueLimits, useSetLimit, type Queue } from '../queue/api'
import {
  type Binding,
  CLUSTER_DEPLOY,
  type Capability,
  useBindCapability,
  useCapabilities,
  useSetWeight,
  useUnbindCapability,
} from './capabilities'

/*
 * Les pipelines que les plugins apportent, et ce que l'instance en règle : leur
 * poids dans le score, combien en mener de front, et leur liaison (quels secrets
 * satisfont leurs besoins, comment leurs volumes sont dimensionnés).
 *
 * Une ligne par pipeline, pour voir d'un coup ce qui est lié, ce qui compte et ce
 * qui tourne à la fois ; les réglages s'ouvrent à la demande. L'écran désigne des
 * secrets, il ne les lit jamais (doc 07 §7.3), et ne nomme pas de compte de
 * service : un pipeline qui déploie se lie dans le fichier de catalogue.
 */

export function PipelinesScreen() {
  const { data, isPending, error, refetch } = useCapabilities()
  const queues = useQueueLimits()
  const [open, setOpen] = useState('')

  if (isPending) return <Skeleton className="h-64 w-full" />
  if (error) {
    return <ErrorState message="Les pipelines n'ont pas pu être chargés." onRetry={() => void refetch()} />
  }
  if (!data) return null

  const queueOf = (kind: string) => queues.data?.queues.find((queue) => queue.kind === kind)
  // Les files sans pipeline : des tâches du dashboard, qui n'ont qu'un plafond.
  const internal = (queues.data?.queues ?? []).filter(
    (queue) => !queue.known && !data.capabilities.some((capability) => capability.ref === queue.kind),
  )
  const selected = data.capabilities.find((capability) => capability.ref === open)

  return (
    <Card title={`Pipelines (${data.capabilities.length})`}>
      <p className="mb-3 text-sm text-zinc-500">
        Un pipeline se lance une fois lié : le fichier de catalogue l&apos;emporte, le reste se lie ici.
        {queues.data && <> Au plus {queues.data.max_concurrent} exécutions à la fois, toutes files confondues (catalogue).</>}
      </p>
      {data.capabilities.length === 0 ? (
        <EmptyState>Aucun pipeline sur cette instance.</EmptyState>
      ) : (
        <table className="w-full text-sm">
          {/* Des variantes `max-sm:` seulement, sans utilitaire d'affichage de base : la
              feuille d'un plugin, chargée après celle-ci, redéfinit `.flex` ou `.hidden`
              et l'emporterait sur une variante `sm:` d'ici. */}
          <thead className="text-left text-xs text-zinc-500 max-sm:hidden">
            <tr>
              <th className="py-1 pr-3 font-medium">Pipeline</th>
              <th className="px-2 py-1 font-medium">Liaison</th>
              <th className="px-2 py-1 font-medium">Score</th>
              <th className="px-2 py-1 font-medium">En parallèle</th>
              <th className="py-1" />
            </tr>
          </thead>
          <tbody>
            {data.capabilities.map((capability) => (
              <PipelineRow
                key={capability.ref}
                capability={capability}
                queue={queueOf(capability.ref)}
                onSettings={() => setOpen(capability.ref)}
              />
            ))}
            {internal.map((queue) => (
              <tr key={queue.kind} className="border-t border-zinc-200 text-zinc-500">
                <td className="py-1.5 pr-3">
                  {queue.label} <span className="text-xs">(tâche interne)</span>
                </td>
                <td className="px-2" />
                <td className="px-2" />
                <td className="px-2">{queue.max_concurrent}</td>
                <td />
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {selected && (
        <Modal title={`Paramètres : ${selected.label}`} onClose={() => setOpen('')}>
          <PipelineSettings
            // La liaison enregistrée entre dans la clé : sans cela, le formulaire
            // garderait le brouillon que le serveur vient de remplacer.
            key={JSON.stringify(selected.binding ?? null)}
            capability={selected}
            queue={queueOf(selected.ref)}
          />
        </Modal>
      )}
    </Card>
  )
}

function bindingLabel(capability: Capability): string {
  if (capability.bound_by === 'file') return 'Fichier'
  if (capability.bound_by === 'database') return 'Ici'
  return 'Non liée'
}

function scoreLabel(capability: Capability): string {
  if (capability.weight !== undefined) return capability.weight === 0 ? 'hors score' : String(capability.weight)
  return capability.default_weight > 0 ? `${capability.default_weight} (nature)` : 'hors score'
}

function PipelineRow({
  capability,
  queue,
  onSettings,
}: {
  capability: Capability
  queue?: Queue
  onSettings: () => void
}) {
  return (
    <tr className="border-t border-zinc-200 max-sm:flex max-sm:flex-wrap max-sm:items-center max-sm:gap-x-3 max-sm:py-1.5">
      <td className="py-1.5 pr-3 max-sm:w-full">
        <span className="font-medium text-zinc-900">{capability.label}</span>
        <span className="ml-2 font-mono text-xs text-zinc-500">{capability.ref}</span>
      </td>
      <td className="px-2 text-zinc-700">
        <Narrow>Liaison</Narrow>
        <span className={capability.bound_by ? '' : 'text-warn-700'}>{bindingLabel(capability)}</span>
        {capability.unavailable && (
          <span title={capability.unavailable} className="ml-1 inline-flex align-middle text-warn-700">
            <Icon name="alert" className="size-3.5" />
            <span className="sr-only">Indisponible : {capability.unavailable}</span>
          </span>
        )}
      </td>
      <td className="px-2 text-zinc-700">
        <Narrow>Score</Narrow>
        {scoreLabel(capability)}
      </td>
      <td className="px-2 text-zinc-700">
        <Narrow>En parallèle</Narrow>
        {queue ? (
          <>
            {queue.max_concurrent}
            {queue.overridden && <span className="text-xs text-zinc-500"> (défaut {queue.default_max_concurrent})</span>}
          </>
        ) : (
          '–'
        )}
      </td>
      <td className="py-1 text-right max-sm:ml-auto">
        <button
          type="button"
          onClick={onSettings}
          aria-label={`Paramètres de ${capability.label}`}
          className="inline-flex items-center gap-1 rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
        >
          <Icon name="cog" className="size-3.5" />
          Paramètres
        </button>
      </td>
    </tr>
  )
}

/** L'intitulé d'une colonne, quand les colonnes s'empilent et que l'en-tête est masqué. */
function Narrow({ children }: { children: ReactNode }) {
  return <span className="mr-1 text-xs text-zinc-500 sm:hidden">{children} :</span>
}

/** Les réglages d'un pipeline : son score, son parallélisme, sa liaison. */
export function PipelineSettings({ capability, queue }: { capability: Capability; queue?: Queue }) {
  return (
    <div className="flex flex-col gap-5">
      <Section title="Score">
        <WeightField capability={capability} />
      </Section>
      {queue && (
        <Section title="Parallélisme">
          <LimitField queue={queue} />
        </Section>
      )}
      <Section title="Liaison">
        <BindingForm capability={capability} />
      </Section>
    </div>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-2">
      <h3 className="text-xs font-medium uppercase tracking-wide text-zinc-500">{title}</h3>
      {children}
    </section>
  )
}

/** Ce que l'on saisit, champ par champ, avant d'en faire une liaison. */
export type BindingDraft = {
  timeout: string
  /** Gardé tel que la liaison l'enregistre, sans champ : le parallélisme se règle
   *  par la file, qui l'emporte à chaud. */
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

const input = 'min-w-0 rounded-md border border-zinc-300 bg-white px-2 py-1 font-mono text-sm text-zinc-900'

/** La liaison d'un pipeline : en lecture seule quand le fichier la porte. */
export function BindingForm({ capability }: { capability: Capability }) {
  const bind = useBindCapability()
  const unbind = useUnbindCapability()
  const [draft, setDraft] = useState(() => draftOf(capability))
  const byFile = capability.bound_by === 'file'
  const deploys = capability.needs.some((need) => need.name === CLUSTER_DEPLOY)
  const failure = bind.error ?? unbind.error

  return (
    <div className="flex flex-col gap-3">
      {capability.unavailable && (
        <p className="text-xs text-warn-700">Indisponible : {capability.unavailable}</p>
      )}

      {byFile ? (
        <p className="text-sm text-zinc-500">
          Le fichier de catalogue lie ce pipeline : sa liaison se modifie avec le déploiement.
        </p>
      ) : (
        <>
          {deploys && (
            <p className="text-xs text-warn-700">
              Il demande <span className="font-mono">{CLUSTER_DEPLOY}</span>, un compte de service : il
              ne peut être lié que par le fichier de catalogue.
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
                    <Select
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
                    >
                      <option value="secretName">Secret nommé</option>
                      <option value="configKey">Clé de configuration</option>
                    </Select>
                    <input
                      id={`${capability.ref}-need-${need.name}`}
                      value={value.value}
                      placeholder={placeholderOf(need.name, value.source)}
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
                  <Select
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
                  >
                    <option value="name">ConfigMap nommée</option>
                    <option value="configKey">Clé de configuration</option>
                  </Select>
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
    </div>
  )
}

/** Ce qu'un besoin attend, en exemple : le nom d'un secret qui le porte, ou la clé qui le nomme. */
const NEED_EXAMPLES: Record<string, { secretName: string; configKey: string }> = {
  'source-read': { secretName: 'git-credentials', configKey: 'project.deployKeySecret' },
  'registry-pull': { secretName: 'regcred', configKey: 'pipeline.imagePullSecret' },
  'registry-push': { secretName: 'regcred', configKey: 'pipeline.registrySecret' },
  'model-access': { secretName: 'arkhitekton-review-model', configKey: '' },
  'forge-read': { secretName: 'arkhitekton-review-forge', configKey: '' },
}

function placeholderOf(need: string, source: 'secretName' | 'configKey'): string {
  return NEED_EXAMPLES[need]?.[source] ?? ''
}

/**
 * Le poids du pipeline dans le score.
 *
 * Hors de la liaison : une capacité liée par le fichier se pondère ici aussi.
 * Vide, elle pèse ce que pèse sa nature ; zéro la met hors score, ses
 * résultats restant affichés.
 */
export function WeightField({ capability }: { capability: Capability }) {
  const setWeight = useSetWeight()
  const [value, setValue] = useState(capability.weight === undefined ? '' : String(capability.weight))
  const fallback =
    capability.default_weight > 0
      ? `${capability.default_weight} (sa nature${capability.group ? ` : ${capability.group}` : ''})`
      : 'hors score'
  const parsed = value.trim() === '' ? null : Number(value)
  const invalid = parsed !== null && (!Number.isFinite(parsed) || parsed < 0 || parsed > 100)
  const unchanged = parsed === (capability.weight ?? null)

  return (
    <form
      className="flex flex-col gap-1"
      onSubmit={(event) => {
        event.preventDefault()
        if (!invalid) setWeight.mutate({ ref: capability.ref, weight: parsed })
      }}
    >
      <label htmlFor={`${capability.ref}-weight`} className="text-sm text-zinc-700">
        Poids dans le score
      </label>
      <div className="flex flex-wrap items-center gap-2">
        <input
          id={`${capability.ref}-weight`}
          type="number"
          min={0}
          max={100}
          step="any"
          value={value}
          placeholder={fallback}
          onChange={(event) => setValue(event.target.value)}
          className="w-56 rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm text-zinc-900"
        />
        <button
          type="submit"
          disabled={invalid || unchanged || setWeight.isPending}
          className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-40"
        >
          {setWeight.isPending ? 'Enregistrement…' : 'Enregistrer'}
        </button>
        {capability.weight !== undefined && (
          <button
            type="button"
            disabled={setWeight.isPending}
            onClick={() => {
              setValue('')
              setWeight.mutate({ ref: capability.ref, weight: null })
            }}
            className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-40"
          >
            Revenir au défaut
          </button>
        )}
      </div>
      <p className="text-xs text-zinc-500">
        Vide : {fallback}. 0 la met hors score : ses résultats s&apos;affichent sans compter.
      </p>
      {setWeight.isError && <p className="text-xs text-critical-700">{(setWeight.error as Error).message}</p>}
    </form>
  )
}

/** Borne du plafond d'une file, alignée sur celle qu'applique l'API. */
const MAX_LIMIT = 20

/**
 * Combien de runs de ce pipeline mener de front.
 *
 * Le plafond réglé ici vit en base et prend effet au tour suivant de
 * l'ordonnanceur ; le défaut du catalogue reste affiché, et y revenir reste
 * possible : un réglage qu'on ne peut pas défaire est un réglage qu'on n'ose pas
 * toucher.
 */
function LimitField({ queue }: { queue: Queue }) {
  const setLimit = useSetLimit()
  const [draft, setDraft] = useState(String(queue.max_concurrent))
  const parsed = Number.parseInt(draft, 10)
  const valid = Number.isFinite(parsed) && parsed >= 1 && parsed <= MAX_LIMIT
  const changed = valid && parsed !== queue.max_concurrent

  return (
    <form
      className="flex flex-col gap-1"
      onSubmit={(event) => {
        event.preventDefault()
        if (changed) setLimit.mutate({ kind: queue.kind, max: parsed })
      }}
    >
      <label htmlFor={`${queue.kind}-limit`} className="text-sm text-zinc-700">
        Maximum en parallèle
      </label>
      <div className="flex flex-wrap items-center gap-2">
        <input
          id={`${queue.kind}-limit`}
          type="number"
          min={1}
          max={MAX_LIMIT}
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
          className="w-20 rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm tabular-nums text-zinc-900"
        />
        <button
          type="submit"
          disabled={!changed || setLimit.isPending}
          className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-40"
        >
          Enregistrer
        </button>
        {queue.overridden && (
          <button
            type="button"
            disabled={setLimit.isPending}
            onClick={() => {
              setDraft(String(queue.default_max_concurrent))
              setLimit.mutate({ kind: queue.kind, max: null })
            }}
            className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-40"
          >
            Revenir au défaut ({queue.default_max_concurrent})
          </button>
        )}
      </div>
      <p className="text-xs text-zinc-500">
        Défaut du catalogue : {queue.default_max_concurrent}
        {queue.default_max_concurrent === 1 && ', une seule à la fois : une contrainte du pipeline lui-même'}. Le
        plafond global de l&apos;instance prime.
      </p>
      {!valid && <p className="text-xs text-critical-700">Attendu entre 1 et {MAX_LIMIT}.</p>}
      {setLimit.isError && <p className="text-xs text-critical-700">Le réglage a été refusé.</p>}
    </form>
  )
}
