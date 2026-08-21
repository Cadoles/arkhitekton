import { NavLink, Outlet } from 'react-router'
import { Icon, type IconName } from '../../components/Icon'

/*
 * Coquille des trois vues d'exécution.
 *
 * Elles répondaient à la même question depuis deux entrées de menu : « qu'est-ce
 * qui tourne, et pourquoi ça n'avance pas ». Séparées, il fallait naviguer de
 * l'une à l'autre pour rapprocher un run de la file qui l'a fait attendre — et
 * les réglages de file n'avaient nulle part où vivre.
 *
 * Les sous-onglets les remettent ensemble sans les mélanger : l'historique des
 * exécutions, l'état des files, et ce qui les gouverne.
 */

type Tab = { to: string; label: string; icon: IconName; end?: boolean }

const TABS: Tab[] = [
  // `end` sur le premier seulement : sans lui, l'onglet des exécutions resterait
  // marqué actif sur les deux autres, qui sont ses descendants dans l'URL.
  { to: '/runs', label: 'Exécutions', icon: 'beaker', end: true },
  { to: '/runs/queue', label: "File d'attente", icon: 'queue' },
  { to: '/runs/settings', label: 'Paramètres', icon: 'cog' },
]

export function ExecutionsLayout() {
  return (
    <div className="flex flex-col gap-4">
      <nav aria-label="Vues d'exécution">
        <ul className="flex gap-1 overflow-x-auto border-b border-zinc-200">
          {TABS.map((tab) => (
            <li key={tab.to}>
              <NavLink
                to={tab.to}
                end={tab.end}
                className={({ isActive }) =>
                  // Même grammaire que la navigation principale : bordure basse
                  // pour l'onglet courant, `aria-current` posé par NavLink — donc
                  // l'information ne repose pas sur la seule couleur.
                  `flex items-center gap-1.5 whitespace-nowrap border-b-2 px-3 py-2 text-sm font-medium transition-colors ${
                    isActive
                      ? 'border-zinc-900 text-zinc-900'
                      : 'border-transparent text-zinc-500 hover:text-zinc-900'
                  }`
                }
              >
                <Icon name={tab.icon} className="size-4" />
                {tab.label}
              </NavLink>
            </li>
          ))}
        </ul>
      </nav>

      <Outlet />
    </div>
  )
}
