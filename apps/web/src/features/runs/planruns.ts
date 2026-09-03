import { useQuery } from '@tanstack/react-query'
import { request } from '../../lib/api'
import { currentProject, projectPath } from '../../lib/project'

/*
 * Ce qu'une automatisation a réellement fait.
 *
 * Un moteur qu'on ne peut pas déboguer est pire qu'un comportement figé, parce
 * qu'on ne sait plus ce qui a eu lieu. Cette vue est la contrepartie de
 * l'éditeur : elle répond à « ma réaction est partie, qu'a-t-elle fait ? ».
 */

export type PlanStep = {
  stage: number
  position: number
  kind: 'action' | 'pipeline'
  ref: string
  /** Le libellé du plan **figé**, et non du catalogue courant : une famille
   *  renommée depuis ne réécrit pas l'histoire. */
  label?: string
  state: 'pending' | 'running' | 'succeeded' | 'failed' | 'skipped'
  /** Rattache un geste de pipeline à son exécution, pour aller lire ses
   *  journaux. */
  job_id?: number
  /** Le motif d'un échec, ou la réponse d'un tiers. Réservé à l'administration. */
  detail?: string
  /** Vrai quand un détail existe et n'est pas montré. Sans lui, un lecteur
   *  croirait qu'il n'y a rien à voir et n'appellerait personne. */
  detail_withheld?: boolean
}

export type PlanStage = { label?: string; steps: PlanStep[] }

export type PlanRun = {
  id: number
  /** `builtin:verification` pour la chaîne intégrée, `reaction:<id>` pour une
   *  réaction déclarée. */
  trigger: string
  event_key: string
  release_id?: number
  state: 'running' | 'succeeded' | 'failed'
  created_at: string
  /** La charge du fait déclencheur, qui a lié les gabarits. Elle explique **ce
   *  sur quoi** l'exécution a porté. */
  context: Record<string, string>
  stages: PlanStage[]
}

export function usePlanRuns() {
  const project = currentProject()

  return useQuery({
    queryKey: ['plan-runs', project],
    queryFn: () =>
      request<{ runs: PlanRun[]; details_visible: boolean }>(projectPath('/plan-runs')),
    enabled: project !== '',
    // Une exécution en cours avance : la relire régulièrement évite d'avoir à
    // recharger la page pour voir une étape se conclure.
    refetchInterval: 10_000,
  })
}

/** Ce que le déclencheur désigne, en français.
 *
 *  La chaîne intégrée porte un nom stable ; une réaction déclarée porte son
 *  identifiant, faute de mieux — son nom vit dans une autre table, et le joindre
 *  pour une ligne de titre coûterait plus qu'il ne rend. */
export function triggerLabel(trigger: string): string {
  if (trigger === 'builtin:verification') return 'Vérifications enchaînées'
  const id = trigger.startsWith('reaction:') ? trigger.slice('reaction:'.length) : ''
  return id ? `Réaction n° ${id}` : trigger
}

export const STEP_LABEL: Record<PlanStep['state'], string> = {
  pending: 'en attente',
  running: 'en cours',
  succeeded: 'réussi',
  failed: 'échoué',
  skipped: 'sauté',
}
