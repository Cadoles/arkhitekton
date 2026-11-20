import { useQuery } from '@tanstack/react-query'
import { request } from '../../lib/api'
import { currentProject } from '../../lib/project'

/*
 * Les statistiques : les releases lancées et les pipelines terminés sur une période, pour le
 * projet courant ou pour tous les projets où l'on est opérateur.
 */

export type StatsRange = '30d' | '90d' | '12m'
export type StatsScope = 'project' | 'all'

export type StatsCount = { key: string; label?: string; count: number }

export type StatsBucket = { start: string; count: number; by_state: Record<string, number> }

export type StatsPipeline = {
  kind: string
  label: string
  runs: number
  succeeded: number
  failed: number
  cancelled: number
  /** Durée médiane des exécutions qui ont démarré, absente sinon. */
  median_seconds?: number
}

export type Stats = {
  range: StatsRange
  bucket: 'week' | 'month'
  since: string
  projects: { slug: string; label: string }[]
  releases: { total: number; by_state: StatsCount[]; by_kind: StatsCount[]; buckets: StatsBucket[] }
  pipelines: StatsPipeline[]
}

export function useStats(scope: StatsScope, range: StatsRange) {
  const project = scope === 'project' ? currentProject() : ''
  return useQuery({
    queryKey: ['stats', scope, project, range],
    queryFn: () => {
      const query = new URLSearchParams({ range })
      if (project) query.set('project', project)
      return request<Stats>(`/stats?${query.toString()}`)
    },
    enabled: scope === 'all' || project !== '',
  })
}
