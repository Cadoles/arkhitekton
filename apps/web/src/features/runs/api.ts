import { useQuery } from '@tanstack/react-query'
import { request } from '../../lib/api'

/*
 * Contrat des exécutions, tel que le sert `internal/http/runs.go`.
 *
 * Les statuts sont ceux du modèle du dashboard, dérivés du **statut** de la
 * condition Tekton et non de son motif : le motif reste affiché, mais aucune
 * décision d'interface ne s'y appuie.
 */

export type RunStatus = 'pending' | 'running' | 'succeeded' | 'failed' | 'cancelled' | 'timedout'

export type Run = {
  id: number
  name: string
  pipeline: string
  status: RunStatus
  reason?: string
  started_at?: string
  completed_at?: string
  /** Un run observé n'a pas été lancé par le dashboard : il s'affiche sans action. */
  observed: boolean
}

export type Task = {
  name: string
  display_name: string
  status: RunStatus
  started_at?: string
  completed_at?: string
  has_log: boolean
  log_sealed: boolean
}

export type RunDetail = { run: Run; tasks: Task[] }

export function useRuns(limit = 100) {
  return useQuery({
    queryKey: ['runs', limit],
    queryFn: () => request<{ runs: Run[] }>(`/runs?limit=${limit}`),
    // La liste est la source de vérité ; le flux SSE ne fait que l'accélérer.
    // Un rafraîchissement de secours couvre le cas où le flux tombe sans que le
    // navigateur le signale.
    refetchInterval: 60_000,
  })
}

export function useRun(name: string) {
  return useQuery({
    queryKey: ['run', name],
    queryFn: () => request<RunDetail>(`/runs/${encodeURIComponent(name)}`),
    // Tant que le run n'a pas conclu, ses étapes sont relues.
    //
    // L'écran d'un run reçoit aussi le flux SSE, qui va plus vite ; la frise
    // affichée dans une modale, elle, n'y est pas abonnée et resterait figée sur
    // la première étape. Le sondage s'arrête de lui-même à la conclusion.
    refetchInterval: (query) =>
      query.state.data && isTerminal(query.state.data.run.status) ? false : 5000,
  })
}

/** Terminal dit qu'il n'y a plus rien à attendre de ce run. */
export function isTerminal(status: RunStatus): boolean {
  return status === 'succeeded' || status === 'failed' || status === 'cancelled' || status === 'timedout'
}

/**
 * Durée d'exécution, en cours ou terminée.
 *
 * `null` quand le run n'a pas démarré : afficher « 0 s » laisserait croire à une
 * exécution instantanée, alors que rien n'a commencé.
 */
export function duration(startedAt?: string, completedAt?: string): number | null {
  if (!startedAt) return null
  const start = new Date(startedAt).getTime()
  const end = completedAt ? new Date(completedAt).getTime() : Date.now()
  if (Number.isNaN(start) || Number.isNaN(end)) return null
  return Math.max(0, end - start)
}

export function formatDuration(milliseconds: number | null): string {
  if (milliseconds === null) return '—'

  const seconds = Math.round(milliseconds / 1000)
  if (seconds < 60) return `${seconds} s`

  const minutes = Math.floor(seconds / 60)
  const remainder = seconds % 60
  if (minutes < 60) return `${minutes} min ${String(remainder).padStart(2, '0')} s`

  return `${Math.floor(minutes / 60)} h ${String(minutes % 60).padStart(2, '0')} min`
}
