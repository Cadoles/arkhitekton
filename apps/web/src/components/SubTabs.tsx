import type { ReactNode } from 'react'
import { NavLink } from 'react-router'

/*
 * Sous-onglets d'un écran découpé.
 *
 * Deux écrans en ont besoin — les exécutions, l'administration — et pour la même
 * raison : ils réunissaient sous un seul titre des choses qui se consultent
 * séparément, et qui s'empilaient donc en une page qu'on parcourait au lieu de la
 * lire.
 *
 * La grammaire est celle de la navigation principale : bordure basse pour
 * l'onglet courant, `aria-current` posé par NavLink — l'information ne repose
 * donc pas sur la seule couleur.
 */

export type SubTab = {
  to: string
  label: string
  icon: ReactNode
  /**
   * `end` sur l'onglet d'index seulement. Sans lui, il resterait marqué actif sur
   * tous les autres, qui sont ses descendants dans l'URL.
   */
  end?: boolean
}

export function SubTabs({ tabs, label }: { tabs: SubTab[]; label: string }) {
  return (
    <nav aria-label={label}>
      <ul className="flex gap-1 overflow-x-auto border-b border-zinc-200">
        {tabs.map((tab) => (
          <li key={tab.to}>
            <NavLink
              to={tab.to}
              end={tab.end}
              className={({ isActive }) =>
                `flex items-center gap-1.5 whitespace-nowrap border-b-2 px-3 py-2 text-sm font-medium transition-colors ${
                  isActive
                    ? 'border-zinc-900 text-zinc-900'
                    : 'border-transparent text-zinc-500 hover:text-zinc-900'
                }`
              }
            >
              {tab.icon}
              {tab.label}
            </NavLink>
          </li>
        ))}
      </ul>
    </nav>
  )
}
