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

export type Capability = {
  ref: string
  label: string
  plugin: string
  needs: CapabilityNeed[]
  /** Les paramètres qu'une liaison peut remplir depuis une clé de configuration. */
  params: string[]
  /** Les volumes créés à chaque run, qu'une liaison dimensionne. */
  volumes: string[]
  bound_by?: 'file' | 'database'
  binding?: Binding
  /** Pourquoi le catalogue ne peut pas la lancer ; vide quand il le peut. */
  unavailable?: string
}

export type SecretSource = { secretName?: string; configKey?: string }

export type Binding = {
  ref: string
  maxConcurrent?: number
  timeout?: string
  params?: Record<string, { key: string }>
  needs?: Record<string, { secret?: SecretSource; serviceAccountName?: string }>
  volumes?: Record<string, { storageClassName?: string; storage?: string }>
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
