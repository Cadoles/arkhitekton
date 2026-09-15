import { projectModules, useSession } from './features/auth/session'
import { mseModule } from './features/mse/module'
import type { AppModule } from './lib/modules'

/*
 * L'assemblage des modules de l'interface.
 *
 * Le seul fichier hors d'un module qui a le droit de l'importer : un test le
 * vérifie (`modules.test.ts`).
 */

/** Les modules que ce build connaît. Une fonction plutôt qu'une constante : les
 *  modules importent des écrans qui importent le noyau, et une liste évaluée au
 *  chargement pourrait l'être avant eux. */
export function appModules(): AppModule[] {
  return [mseModule]
}

/** Les modules allumés pour le projet courant. Vide tant que la session n'est pas
 *  chargée. */
export function useActiveModules(): AppModule[] {
  const session = useSession()
  if (session.kind !== 'authenticated') return []
  const enabled = projectModules(session.identity)
  return appModules().filter((module) => enabled.includes(module.id))
}
