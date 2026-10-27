import { Navigate, type RouteObject } from 'react-router'
import { ModuleRoute } from './components/ModuleRoute'
import { ScreenView } from './components/Screen'
import { appModules } from './modules'
import type { Screen, UIInterface } from './lib/interface'

/*
 * Les routes, construites depuis les écrans de l'interface composée par le
 * serveur (`internal/ui/resources` pour le noyau, plus ce que les plugins allumés
 * apportent).
 *
 * Un écran s'inscrit sous son parent : la coquille encadre tous les autres, et
 * les écrans à onglets encadrent leurs onglets. Sans chemin, il est l'index de
 * son parent ; avec `redirect`, il renvoie ailleurs une adresse conservée pour
 * les liens déjà partagés.
 */

function toRoute(screen: Screen, screens: Screen[]): RouteObject {
  if (screen.redirect) {
    return { path: screen.path, element: <Navigate to={screen.redirect} replace /> }
  }
  const element = <ScreenView screen={screen} />
  if (!screen.path) return { index: true, element }
  const children = childrenOf(screen.name, screens)
  return { path: screen.path, element, ...(children.length > 0 ? { children } : {}) }
}

function childrenOf(parent: string, screens: Screen[]): RouteObject[] {
  return screens.filter((screen) => screen.parent === parent).map((screen) => toRoute(screen, screens))
}

/** Les routes de l'interface, avec celles des modules connus à cet instant :
 *  appelée une fois l'interface et les scripts des plugins chargés. */
export function buildRoutes(ui: UIInterface): RouteObject[] {
  const roots = ui.screens.filter((screen) => !screen.parent).map((screen) => toRoute(screen, ui.screens))
  // Les écrans des modules d'avant les écrans en données, chacun gardé par son
  // interrupteur : éteint, une URL qui y mène renvoie à l'accueil.
  const modules = appModules().flatMap((module) =>
    (module.routes ?? []).map((route) => ({
      ...route,
      element: <ModuleRoute module={module.id}>{route.element}</ModuleRoute>,
    })),
  )
  const shell = roots.find((route) => route.path === '/')
  if (shell && modules.length > 0) shell.children = [...modules, ...(shell.children ?? [])]
  return roots
}
