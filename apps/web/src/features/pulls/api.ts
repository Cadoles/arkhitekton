import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request } from '../../lib/api'
import { projectPath } from '../../lib/project'
import type { ReleaseJob } from '../releases/api'

export type PullState = 'open' | 'closed' | 'merged'

/** Une pull request, telle que le worker la suit sur la forge. */
export type PullRequest = {
  number: number
  title: string
  author: string
  source_branch: string
  target_branch: string
  head_sha: string
  state: PullState
  url?: string
  created_at?: string
  updated_at?: string
  closed_at?: string
  merged_at?: string
  /** L'étiquette sous laquelle la tête de la PR est construite : `pr-<n>-<commit>`. */
  tag: string
}

/** Un plan ouvert par un fait de la PR, réduit à ce que la page en montre. */
export type PullPlanRun = {
  id: number
  trigger: string
  state: string
  created_at: string
  stages: { label?: string; steps: { ref: string; label?: string; kind: string; state: string }[] }[]
}

export type PullDetail = {
  pull: PullRequest
  jobs: ReleaseJob[]
  plan_runs: PullPlanRun[]
}

/** Le filtre de la liste : `closed` comprend les PR fusionnées. */
export type PullFilter = 'open' | 'closed' | 'all'

export function usePulls(filter: PullFilter) {
  return useQuery({
    queryKey: ['pulls', 'list', filter],
    queryFn: () =>
      request<{ pulls: PullRequest[] }>(projectPath(filter === 'all' ? '/pulls' : `/pulls?state=${filter}`)),
    // La liste suit la forge à la minute : la relire de temps en temps suffit à voir arriver une PR.
    refetchInterval: 60_000,
  })
}

export function usePull(number: number | undefined) {
  return useQuery({
    queryKey: ['pulls', number],
    queryFn: () => request<PullDetail>(projectPath(`/pulls/${number}`)),
    enabled: number !== undefined && Number.isFinite(number),
    // Les exécutions avancent pendant qu'on lit : la page se relit tant qu'elle est ouverte.
    refetchInterval: 10_000,
  })
}

/** Lance un pipeline sur la tête de la PR. Le serveur remplit la révision, l'étiquette et les
 *  images de la PR ; `params` porte le reste. */
export function useLaunchOnPull(number: number) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ kind, params }: { kind: string; params: Record<string, string> }) =>
      request<{ id: number; kind: string; params: Record<string, string> }>(projectPath(`/pulls/${number}/launch`), {
        method: 'POST',
        body: JSON.stringify({ kind, params }),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['pulls', number] })
      void queryClient.invalidateQueries({ queryKey: ['queue'] })
    },
  })
}
