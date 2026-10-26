import { projectModules, useSession } from './features/auth/session'
import type { AppModule } from './lib/modules'
import { registeredModules } from './lib/registry'

/*
 * L'assemblage des modules de l'interface.
 *
 * Une distribution dépose chaque module dans `src/plugins/<id>/`, qui exporte
 * par défaut sa définition depuis `index.tsx`. Ce fichier est le seul, hors des
 * modules, à les importer : un test le vérifie (`modules.test.ts`).
 */

const discovered = import.meta.glob<{ default: AppModule }>('./plugins/*/index.tsx', {
  eager: true,
})

/** Les modules que ce build connaît, dans l'ordre de leur répertoire, puis
 *  ceux des plugins installés. Une fonction plutôt qu'une constante : les modules
 *  importent des écrans qui importent le noyau, et une liste évaluée au
 *  chargement pourrait l'être avant eux ; ceux des plugins arrivent après. */
export function appModules(): AppModule[] {
  const built = Object.entries(discovered)
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([, loaded]) => loaded.default)
  return [...built, ...registeredModules()]
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
