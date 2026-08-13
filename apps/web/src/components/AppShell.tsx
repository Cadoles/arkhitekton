import { NavLink, Outlet } from 'react-router'
import { Icon, type IconName } from './Icon'
import { useTheme } from '../lib/theme'
import { logout } from '../lib/api'
import { roleLabel, useSession } from '../features/auth/session'

/*
 * Coquille de l'application : barre supérieure et navigation.
 *
 * La barre reprend celle de la maquette — même hauteur, même pastille de logo,
 * même largeur de conteneur. Elle en diffère sur un point : la maquette n'a qu'un
 * écran, donc pas de navigation. Celle-ci s'insère sous la barre, dans la même
 * grammaire.
 */

type NavEntry = { to: string; label: string; icon: IconName }

const NAV: NavEntry[] = [
  { to: '/environments', label: 'Environnements', icon: 'layers' },
  { to: '/versions', label: 'Versions', icon: 'tag' },
  { to: '/releases', label: 'Releases', icon: 'doc' },
  { to: '/runs', label: 'Exécutions', icon: 'beaker' },
  { to: '/queue', label: "File d'attente", icon: 'queue' },
  { to: '/admin', label: 'Administration', icon: 'cog' },
]

function ThemeToggle() {
  const { isDark, toggle } = useTheme()

  return (
    <button
      type="button"
      role="switch"
      aria-checked={isDark}
      aria-label="Basculer le thème sombre"
      onClick={toggle}
      className="relative inline-flex h-7 w-12 shrink-0 items-center rounded-full border border-zinc-200 bg-zinc-100 transition-colors"
    >
      <span
        className={`absolute left-0.5 grid size-5 place-items-center rounded-full bg-white text-zinc-600 shadow-sm transition-transform ${
          isDark ? 'translate-x-5' : 'translate-x-0'
        }`}
      >
        <Icon name={isDark ? 'moon' : 'sun'} className="size-3.5" />
      </span>
    </button>
  )
}

function SessionBadge() {
  const session = useSession()

  if (session.kind !== 'authenticated') return null

  return (
    <div className="flex items-center gap-2">
      <span className="hidden items-center gap-1.5 text-xs text-zinc-600 sm:flex">
        <Icon name="user" className="size-3.5" />
        {/* Les espaces sont explicites : sans eux, JSX colle les nœuds texte et
            les lecteurs d'écran annoncent « vcarroyAdministrateur ». */}
        {session.identity.username}
        <span className="text-zinc-400" aria-hidden="true">
          {' · '}
        </span>
        <span className="sr-only">, rôle </span>
        {roleLabel(session.identity.role)}
      </span>
      <button
        type="button"
        onClick={() => void logout()}
        className="rounded-md border border-zinc-200 px-2 py-1 text-xs font-medium text-zinc-600 transition-colors hover:bg-zinc-100"
      >
        Déconnexion
      </button>
    </div>
  )
}

export function AppShell() {
  return (
    <div className="min-h-screen">
      <header className="border-b border-zinc-200 bg-white">
        <div className="mx-auto flex max-w-6xl flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3">
          <div className="flex items-center gap-2">
            <span className="grid size-8 place-items-center rounded-md bg-zinc-900 text-white">
              <Icon name="gauge" className="size-5" />
            </span>
            <span className="text-lg font-semibold text-zinc-900">Dashboard de livraisons</span>
          </div>

          <div className="ml-auto flex items-center gap-3">
            <SessionBadge />
            <ThemeToggle />
          </div>
        </div>

        <nav aria-label="Navigation principale" className="border-t border-zinc-200">
          <ul className="mx-auto flex max-w-6xl gap-1 overflow-x-auto px-4">
            {NAV.map((entry) => (
              <li key={entry.to}>
                <NavLink
                  to={entry.to}
                  className={({ isActive }) =>
                    // La bordure basse marque l'onglet courant. `aria-current`
                    // est posé par NavLink : l'information ne repose donc pas
                    // sur la seule couleur.
                    `flex items-center gap-1.5 whitespace-nowrap border-b-2 px-3 py-2 text-sm font-medium transition-colors ${
                      isActive
                        ? 'border-zinc-900 text-zinc-900'
                        : 'border-transparent text-zinc-500 hover:text-zinc-900'
                    }`
                  }
                >
                  <Icon name={entry.icon} className="size-4" />
                  {entry.label}
                </NavLink>
              </li>
            ))}
          </ul>
        </nav>
      </header>

      <main className="mx-auto max-w-6xl px-4 py-6">
        <Outlet />
      </main>
    </div>
  )
}
