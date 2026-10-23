import { useQuery } from '@tanstack/react-query'
import { request } from '../../lib/api'
import { currentProject, projectPath } from '../../lib/project'

/*
 * Le fil des faits d'un projet.
 *
 * En lecture seule, et personne n'y réagit encore. Ce qu'il apporte est un moyen
 * de répondre à la question qui viendra ensuite — « pourquoi rien ne s'est
 * passé ? » — dont les deux réponses ne se corrigent pas de la même façon : ou
 * bien le fait n'a pas été émis, ou bien aucune réaction ne l'appariait.
 *
 * Sans le fil, les deux sont indiscernables.
 */

export type ProjectEvent = {
  id: number
  /** `release.created`, `gitea.pull_request.opened`… Préfixé de son plugin quand
   *  il n'est pas du noyau. */
  type: string
  /** Qui l'a constaté : l'identifiant du plugin, ou vide quand le noyau l'a
   *  constaté lui-même. Ce n'est pas tout à fait le préfixe du type —
   *  `version.created` est du noyau et porte pourtant la sonde qui l'a vu. */
  source?: string
  /** Ce sur quoi porte le fait, dans le vocabulaire de l'écran : un tag, un nom
   *  de run. */
  subject?: string
  /** Rendue telle quelle : ses clés dépendent du type, et une liste codée ici
   *  laisserait invisibles celles qu'un plugin déclarera. */
  payload: Record<string, string>
  occurred_at: string
  /** Présent seulement quand il **diffère** de l'instant du fait — ce qui
   *  n'arrive pas encore, et arrivera quand des sondes rattraperont de
   *  l'historique. */
  recorded_at?: string
  /** Profondeur d'enchaînement. Toujours nulle tant que rien ne réagit. */
  depth: number
}

export function useProjectEvents() {
  const project = currentProject()

  return useQuery({
    // Le projet fait partie de la clé : sans lui, changer de projet afficherait
    // le fil du précédent jusqu'au retour du réseau.
    queryKey: ['events', project],
    queryFn: () => request<{ events: ProjectEvent[] }>(projectPath('/events')),
    enabled: project !== '',
  })
}

/** Ce que chaque type de fait désigne, en français.
 *
 *  Les faits d'un module prennent le libellé que le module leur donne
 *  (`eventLabels`). Un type absent des deux s'affiche **tel quel** plutôt que de
 *  disparaître : une table fermée rendrait muets ceux qu'aucun ne nomme. */
export const EVENT_LABEL: Record<string, string> = {
  'version.created': 'Version déclarée dans le suivi',
  'release.created': 'Release déclarée',
  'release.released': 'Release validée',

  /* Les faits retirés par la réduction (doc 15 §15.3). Plus personne ne les émet,
   * mais le fil est une **histoire** : les lignes déjà écrites restent, et les
   * retirer d'ici les ferait s'afficher en brut. */
  'release.built': 'Construction conclue',
  'release.verified': 'Vérifications conclues',
  'pipeline.finished': 'Exécution terminée',
  /* Devenu `mse.release.promoted` quand la promotion est passée au plugin (doc 16). */
  'release.promoted': 'Promotion aboutie',
}

/** L'issue portée par la charge, quand la question se pose. */
export function outcomeOf(event: ProjectEvent): string {
  return event.payload.outcome ?? ''
}
