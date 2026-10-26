import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request } from '../../lib/api'

/*
 * Les plugins installés depuis un registre.
 *
 * L'écran demande, le worker tire : une installation passe par la file, vérifie
 * la signature contre les clés de l'exploitation, puis rejoint les capacités en
 * vigueur. L'écran suit donc un état, et le relit tant qu'une demande attend.
 */

export type InstallationState = 'pending' | 'installed' | 'failed' | 'removed'

export type Installation = {
  id: number
  reference: string
  digest: string
  plugin?: string
  version?: string
  /** Empreinte de la clé de confiance qui a signé. */
  signed_by?: string
  state: InstallationState
  error?: string
  /** La version avec laquelle partent les nouveaux runs. */
  active: boolean
  requested_by: string
  requested_at: string
  installed_at?: string
  removed_at?: string
}

const KEY = ['admin', 'plugins', 'installations']

export function useInstallations() {
  return useQuery({
    queryKey: KEY,
    queryFn: () => request<{ installations: Installation[] }>('/admin/plugins/installations'),
    // Une demande se règle dans le worker, hors de toute requête : sans relecture,
    // elle resterait affichée « en attente » jusqu'au prochain rechargement.
    refetchInterval: (query) =>
      query.state.data?.installations.some((installation) => installation.state === 'pending')
        ? 3000
        : false,
  })
}

/** Ce qu'une installation change : la liste des plugins et leurs capacités. */
function useInvalidate() {
  const queryClient = useQueryClient()
  return () => {
    void queryClient.invalidateQueries({ queryKey: ['admin', 'plugins'] })
    void queryClient.invalidateQueries({ queryKey: ['admin', 'capabilities'] })
  }
}

export function useRequestInstallation() {
  const invalidate = useInvalidate()
  return useMutation({
    mutationFn: (reference: string) =>
      request<{ id: number; job: number; reference: string }>('/admin/plugins/installations', {
        method: 'POST',
        body: JSON.stringify({ reference }),
      }),
    onSuccess: invalidate,
  })
}

export function useActivateInstallation() {
  const invalidate = useInvalidate()
  return useMutation({
    mutationFn: (id: number) =>
      request<void>(`/admin/plugins/installations/${id}/activate`, {
        method: 'POST',
      }),
    onSuccess: invalidate,
  })
}

export function useRemoveInstallation() {
  const invalidate = useInvalidate()
  return useMutation({
    mutationFn: (id: number) =>
      request<{ uninstalled: string }>(`/admin/plugins/installations/${id}`, {
        method: 'DELETE',
      }),
    onSuccess: invalidate,
  })
}
