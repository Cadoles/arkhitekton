import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request } from '../../lib/api'

export type JobState = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'

/** Motifs d'attente, tels que la base les calcule. */
export type QueueReason = 'class-busy' | 'capacity' | 'draining'

export type Job = {
  id: number
  kind: string
  label: string
  queue_class: string
  priority: number
  state: JobState
  requested_by: string
  requested_at: string
  started_at?: string
  attempt: number
  queue_reason?: QueueReason
  run_name?: string
}

export type Kind = {
  kind: string
  label: string
  queue_class: string
  required_inputs: string[]
}

type QueueResponse = { jobs: Job[]; kinds: Kind[]; max_concurrent: number }

export function useQueue() {
  return useQuery({
    queryKey: ['queue'],
    queryFn: () => request<QueueResponse>('/queue'),
    // La file bouge en permanence, et son intérêt est précisément de dire ce qui
    // se passe maintenant. Deux secondes suivent le rythme de l'ordonnanceur.
    refetchInterval: 2000,
  })
}

export function useLaunch() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (payload: { kind: string; params: Record<string, string> }) =>
      request<{ id: number }>('/queue', { method: 'POST', body: JSON.stringify(payload) }),
    onSuccess: () => {
      // La file est rafraîchie sans attendre le prochain intervalle : voir son
      // job apparaître est ce qui confirme que le lancement a été pris.
      void queryClient.invalidateQueries({ queryKey: ['queue'] })
    },
  })
}

export function useCancel() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (id: number) => request<{ state: string }>(`/queue/${id}/cancel`, { method: 'POST' }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['queue'] })
    },
  })
}

/**
 * Explication d'une attente, en français et à la première personne de la file.
 *
 * Le texte dit **ce qui bloque**, pas seulement qu'il y a blocage : une file qui
 * n'explique pas son attente engendre systématiquement des relances inutiles.
 */
export function explainWait(job: Job, kinds: Kind[]): string {
  switch (job.queue_reason) {
    case 'class-busy': {
      const other = kinds.find((entry) => entry.queue_class === job.queue_class && entry.kind !== job.kind)
      const shared = other ? ` (partagée avec ${other.label})` : ''
      return `La classe ${job.queue_class} est déjà occupée${shared}.`
    }
    case 'capacity':
      return 'Le plafond d’exécutions simultanées est atteint.'
    case 'draining':
      // Le cas le plus opaque, et le plus long : sans explication, l'utilisateur
      // relance en croyant à une panne.
      return 'Cluster réservé pour une exécution exclusive — attente de la fin des runs en cours.'
    default:
      return 'En attente de son tour.'
  }
}
