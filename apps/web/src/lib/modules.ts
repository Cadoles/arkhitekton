import type { ComponentType } from 'react'
import type { RouteObject } from 'react-router'
import type { IconName } from '../components/Icon'
import type { Release, ReleaseDetail } from '../features/releases/api'

/*
 * Ce qu'un module apporte à l'interface.
 *
 * Le noyau ne nomme aucun module : il lit ces définitions, et un module éteint
 * n'apporte rien. Seul `src/modules.ts` les assemble, comme le paquet `app`
 * côté serveur : il trouve ceux que la distribution a déposés dans
 * `src/plugins/<id>/` (doc 06 §6.13).
 */

export type ModuleNavEntry = {
  to: string
  label: string
  icon: IconName
}

export type AppModule = {
  /** L'identifiant annoncé par `/me` quand le module est allumé. */
  id: string
  /** La page d'accueil que le module propose. Le premier module allumé qui en
   *  déclare une l'emporte ; sans module, l'accueil est Releases. */
  home?: string
  nav?: ModuleNavEntry[]
  /** Les écrans du module. Montés sous la coquille, et renvoyés à l'accueil quand
   *  le module est éteint. */
  routes?: RouteObject[]
  /** Les sections ajoutées à l'écran d'une release, après ses artefacts. */
  releaseSections?: ComponentType<{ release: ReleaseDetail }>[]
  /** Les pastilles ajoutées à côté du nom d'une release. */
  releaseBadges?: ComponentType<{ release: Release }>[]
  /** Les libellés des faits que le plugin émet, par type. Lus même module
   *  éteint : le fil des faits est une histoire. */
  eventLabels?: Record<string, string>
}
