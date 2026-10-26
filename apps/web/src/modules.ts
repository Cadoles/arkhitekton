import { projectModules, useSession } from './features/auth/session'
import type { AppModule } from './lib/modules'
import { registeredModules } from './lib/registry'

/*
 * L'assemblage des modules de l'interface : ceux que les plugins installés
 * enregistrent en se chargeant (`lib/runtime.ts`). Le noyau n'en importe aucun ;
 * un plugin, déposé dans `src/plugins/<id>/` le temps de construire son script,
 * n'importe du noyau que `@arkhitekton/host` (`modules.test.ts`).
 */

/** Les modules des plugins installés, dans l'ordre de leur chargement. Une
 *  fonction plutôt qu'une constante : ils arrivent après le chargement du noyau. */
export function appModules(): AppModule[] {
  return registeredModules()
}

/** Le libellé qu'un module donne à un type de fait. */
export function moduleEventLabel(type: string): string | undefined {
  for (const module of appModules()) {
    const label = module.eventLabels?.[type]
    if (label) return label
  }
  return undefined
}

/** Les modules allumés pour le projet courant. Vide tant que la session n'est pas
 *  chargée. */
export function useActiveModules(): AppModule[] {
  const session = useSession()
  if (session.kind !== 'authenticated') return []
  const enabled = projectModules(session.identity)
  return appModules().filter((module) => enabled.includes(module.id))
}
