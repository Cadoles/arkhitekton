import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request } from '../../lib/api'
import type { BenchStatus } from '../../lib/status'

export type BenchMeasure = {
  id: number
  metric: string
  label: string
  unit: string
  value: number
  lower_better: boolean
  volume: boolean
  baseline?: number
  /** Date de la campagne de référence : une baseline vieillit, et savoir de
   *  quand elle date fait partie du verdict. */
  baseline_at?: string
  verdict: BenchStatus
  ratio?: number
  /** Verdict rendu par k6 sur ses propres seuils. Il dit « le contrat est tenu »
   *  là où la baseline dit « c'est aussi bon qu'avant » : les deux peuvent se
   *  contredire, et l'écran montre les deux. */
  threshold_ok?: boolean
}

export type Campaign = {
  id: number
  scenario: string
  exclusive: boolean
  concurrent_jobs: number
  /** Faux quand les conditions de mesure interdisent la comparaison. La campagne
   *  reste affichée : la cacher ferait croire qu'elle n'a pas eu lieu (B16). */
  comparable: boolean
  reason?: string
  duration_ms: number
  summary?: string
  measured_at: string
  verdict: BenchStatus
  measures: BenchMeasure[]
  /** Tendance par métrique, du plus ancien au plus récent. Seules les mesures
   *  comparables y figurent — une courbe qui mêle des conditions différentes est
   *  une tendance inventée. */
  history?: Record<string, number[]>
}

export function useBench(tag: string | undefined) {
  return useQuery({
    queryKey: ['releases', tag, 'bench'],
    queryFn: () => request<{ campaigns: Campaign[] }>(`/releases/${encodeURIComponent(tag as string)}/bench`),
    enabled: Boolean(tag),
  })
}

export function usePinBaseline(tag: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (measure: number) =>
      request<{ measure_id: number }>(
        `/releases/${encodeURIComponent(tag)}/bench/${measure}/baseline`,
        { method: 'POST' },
      ),
    onSuccess: () => {
      // Tous les verdicts de ce scénario changent : la référence a bougé.
      void queryClient.invalidateQueries({ queryKey: ['releases', tag, 'bench'] })
    },
  })
}
