import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request } from '../../lib/api'

/*
 * Les liaisons des capacités de pipeline.
 *
 * Le fichier de catalogue de l'exploitation l'emporte : ce qu'il lie s'affiche
 * en lecture seule. Ce que l'écran lie complète le fichier, et ne nomme jamais un
 * compte de service : accorder une identité de cluster reste un acte de
 * déploiement, versionné avec lui.
 */

export type CapabilityNeed = { name: string; reason?: string }

/** Un espace de travail rempli par une ConfigMap de l'exploitation : une politique,
 *  un barème, un jeu de règles. Ce n'est pas un besoin, puisque le monter n'accorde
 *  rien ; la raison dit quoi y mettre. */
export type CapabilityConfigMap = { workspace: string; reason?: string }

export type Capability = {
  ref: string
  label: string
  plugin: string
  needs: CapabilityNeed[]
  /** Les paramètres qu'une liaison peut remplir depuis une clé de configuration. */
  params: string[]
  /** Les volumes créés à chaque run, qu'une liaison dimensionne. */
  volumes: string[]
  /** Les espaces de travail qu'une liaison remplit avec une ConfigMap. */
  config_maps: CapabilityConfigMap[]
  bound_by?: 'file' | 'database'
  binding?: Binding
  /** Pourquoi le catalogue ne peut pas la lancer ; vide quand il le peut. */
  unavailable?: string
  /** Le poids réglé dans le score ; absent, elle pèse `default_weight`. */
  weight?: number
  /** Le poids de sa nature, zéro quand le score ne la connaît pas : hors score. */
  default_weight: number
  /** La nature de ses résultats : `unit`, `security`, `accessibility`… */
  group?: string
}

export type SecretSource = { secretName?: string; configKey?: string }

/** Les deux formes d'une ConfigMap liée, jamais les deux à la fois. */
export type ConfigMapSource = { name?: string; configKey?: string }

export type Binding = {
  ref: string
  maxConcurrent?: number
  timeout?: string
  params?: Record<string, { key: string }>
  needs?: Record<string, { secret?: SecretSource; serviceAccountName?: string }>
  volumes?: Record<string, { storageClassName?: string; storage?: string }>
  configMaps?: Record<string, ConfigMapSource>
}

/** Le seul besoin qui soit une identité de cluster : il se lie dans le fichier. */
export const CLUSTER_DEPLOY = 'cluster-deploy'

const KEY = ['admin', 'capabilities']

export function useCapabilities() {
  return useQuery({
    queryKey: KEY,
    queryFn: () => request<{ capabilities: Capability[] }>('/admin/capabilities'),
  })
}

function bindingPath(ref: string) {
  const [plugin = '', name = ''] = ref.split('/')
  return `/admin/capabilities/${encodeURIComponent(plugin)}/${encodeURIComponent(name)}/binding`
}

export function useBindCapability() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (binding: Binding) =>
      request<{ ref: string }>(bindingPath(binding.ref), {
        method: 'PUT',
        body: JSON.stringify(binding),
      }),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: KEY }),
  })
}

export function useUnbindCapability() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (ref: string) => request<void>(bindingPath(ref), { method: 'DELETE' }),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: KEY }),
  })
}

function weightPath(ref: string) {
  return bindingPath(ref).replace(/\/binding$/, '/weight')
}

/** Règle le poids d'un pipeline dans le score, ou le rend à sa nature (`null`). */
export function useSetWeight() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ ref, weight }: { ref: string; weight: number | null }) =>
      weight === null
        ? request<void>(weightPath(ref), { method: 'DELETE' })
        : request<unknown>(weightPath(ref), { method: 'PUT', body: JSON.stringify({ weight }) }),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: KEY }),
  })
}
