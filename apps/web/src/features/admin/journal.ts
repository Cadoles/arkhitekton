import { useQuery } from '@tanstack/react-query'
import { request } from '../../lib/api'

/*
 * Le journal : ce qui s'est passé dans l'application.
 *
 * Deux natures de lignes. Une **action** dit qui a fait quoi ; un **événement**
 * dit ce qui s'est passé, quel qu'en soit l'auteur : une poussée sur la forge
 * comme une release créée. Un événement porte les automatisations qu'il a
 * déclenchées, ce qui répond à « pourquoi rien ne s'est passé ? » : ou bien
 * l'événement n'a pas eu lieu, ou bien rien n'y a réagi.
 */

export type JournalNature = 'action' | 'event'

export type JournalAutomation = {
  /** `reaction:<id>`, `schedule:<id>` ou `builtin:<nom>`. */
  trigger: string
  /** Le nom d'une réaction déclarée. */
  name?: string
  state: 'running' | 'succeeded' | 'failed'
}

export type JournalEntry = {
  nature: JournalNature
  id: number
  at: string
  project?: string
  payload?: Record<string, unknown>

  actor?: string
  action?: string
  target?: string

  /** `release.created`, `gitea.pull_request.opened`… */
  type?: string
  /** Le plugin qui a constaté l'événement, vide pour le noyau. */
  source?: string
  subject?: string
  /** Présent seulement quand il diffère de l'instant de l'événement. */
  recorded_at?: string
  /** Reçu, pas encore routé vers les automatisations. */
  pending?: boolean
  automations?: JournalAutomation[]
}

export type JournalFilter = {
  nature: '' | JournalNature
  project: string
  search: string
  limit: number
}

export function useJournal(filter: JournalFilter) {
  const query = new URLSearchParams()
  if (filter.nature) query.set('nature', filter.nature)
  if (filter.project) query.set('project', filter.project)
  if (filter.search) query.set('q', filter.search)
  query.set('limit', String(filter.limit))

  return useQuery({
    queryKey: ['admin', 'journal', filter],
    queryFn: () => request<{ entries: JournalEntry[] }>(`/admin/journal?${query.toString()}`),
    // Garder la page affichée pendant qu'on élargit la recherche : « Plus ancien »
    // ne doit pas vider l'écran avant de le remplir.
    placeholderData: (previous) => previous,
  })
}

/** Ce que chaque type d'événement désigne, en français.
 *
 *  Les événements d'un plugin prennent le libellé que son manifeste leur donne
 *  (`eventLabels`). Un type absent des deux s'affiche **tel quel** plutôt que de
 *  disparaître : un journal qui cache des lignes fait croire qu'il ne s'est rien
 *  passé. */
export const EVENT_LABEL: Record<string, string> = {
  'version.created': 'Version déclarée dans le suivi',
  'release.drafted': 'Brouillon de release créé',
  'release.created': 'Release lancée',
  'release.released': 'Release validée',
  'pull_request.opened': 'Pull request ouverte',
  'pull_request.synchronized': 'Pull request mise à jour',
  'pull_request.ready': 'Pull request prête à relire',
  'pull_request.reopened': 'Pull request rouverte',
  'pull_request.closed': 'Pull request fermée',
  'pull_request.merged': 'Pull request fusionnée',

  /* Les faits retirés par la réduction (doc 15 §15.3). Plus personne ne les émet,
   * mais le journal est une **histoire** : les lignes déjà écrites restent. */
  'release.built': 'Construction conclue',
  'release.verified': 'Vérifications conclues',
  'pipeline.finished': 'Exécution terminée',
  /* Devenu `mse.release.promoted` quand la promotion est passée au plugin (doc 16). */
  'release.promoted': 'Promotion aboutie',
}

/** L'issue portée par la charge d'un événement, quand la question se pose. */
export function outcomeOf(entry: JournalEntry): string {
  const outcome = entry.payload?.outcome
  return typeof outcome === 'string' ? outcome : ''
}

/** Le nom d'une automatisation déclenchée : celui de la réaction, sinon celui
 *  de son déclencheur. */
export function automationLabel(automation: JournalAutomation): string {
  if (automation.name) return automation.name
  if (automation.trigger === 'builtin:release') return 'Automatisation de release'
  if (automation.trigger === 'builtin:verification') return 'Vérifications enchaînées'
  if (automation.trigger.startsWith('schedule:')) return `Planification n° ${automation.trigger.slice('schedule:'.length)}`
  if (automation.trigger.startsWith('reaction:')) return `Réaction n° ${automation.trigger.slice('reaction:'.length)}`
  return automation.trigger
}
