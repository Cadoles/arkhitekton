import { Suspense } from 'react'
import { NavLink, Outlet } from 'react-router'
import { Icon, type IconName } from './Icon'
import { Skeleton } from './primitives'
import { useTheme } from '../lib/theme'
import { logout } from '../lib/api'
import { roleLabel, useSession } from '../features/auth/session'
import { currentProject, projects, selectProject } from '../lib/project'

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
  // Une entrée pour les trois vues d'exécution : l'historique, les files et
  // leurs réglages sont des sous-onglets, pas des destinations distinctes.
  { to: '/runs', label: 'Exécutions', icon: 'beaker' },
  { to: '/admin', label: 'Administration', icon: 'cog' },
]

/**
 * Choix du projet dont les écrans de releases et de versions parlent.
 *
 * Rendu seulement au-delà d'un projet : proposer un choix entre une seule option
 * est du bruit, et l'immense majorité des déploiements n'en aura qu'un.
 *
 * Le changement recharge la page plutôt que d'invalider les requêtes une à une.
 * C'est brutal et c'est assumé : le projet courant est lu dans un module, hors de
 * tout arbre React, et une invalidation partielle laisserait des écrans montrant
 * les données de l'ancien projet sous les URL du nouveau — un écart bien pire
 * qu'un rechargement.
 */
function ProjectPicker() {
  const choices = projects()
  if (choices.length < 2) return null

  return (
    <label className="flex items-center gap-2 text-sm">
      <span className="sr-only">Projet</span>
      <select
        value={currentProject()}
        onChange={(event) => {
          selectProject(event.target.value)
          window.location.reload()
        }}
        className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm text-zinc-900"
      >
        {choices.map((choice) => (
          <option key={choice.slug} value={choice.slug}>
            {choice.label || choice.slug}
          </option>
        ))}
      </select>
    </label>
  )
}

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

/**
 * Identifiant du conteneur de défilement de l'application.
 *
 * Partagé avec `useScrollLock` : c'est lui qu'une modale doit figer, le document
 * ne défilant plus.
 */
export const APP_SCROLL_ID = 'app-scroll'

export function AppShell() {
  return (
    /*
     * Le défilement appartient au conteneur intérieur, pas au document.
     *
     * Réserver la gouttière de l'ascenseur sur `<html>` évitait bien le décalage
     * du contenu centré, mais découpait une bande hors de **tout** ce qui vit
     * dedans : le bandeau de l'en-tête s'arrêtait avant le bord droit, et le
     * voile d'une modale — `position: fixed`, donc borné au bloc conteneur
     * initial — n'atteignait pas cette bande non plus. D'où un trou clair à
     * droite de l'en-tête, et une bande sombre le long des modales.
     *
     * En descendant l'ascenseur d'un cran, il devient un élément de la mise en
     * page : l'en-tête traverse toute la largeur, les surfaces `fixed` couvrent
     * la fenêtre entière, et la gouttière réservée ici garde le contenu centré
     * immobile quand l'ascenseur va et vient.
     *
     * La coquille est `fixed` et non haute de `100dvh`. Les deux se valent tant
     * que rien ne réduit la zone visible, mais `100dvh` est la hauteur de la
     * fenêtre et non celle qui reste : qu'un ascenseur horizontal apparaisse et
     * la coquille dépasse de sa hauteur, le document se met à défiler, et
     * l'écran porte alors **deux** ascenseurs verticaux — celui de la page,
     * en-tête compris, et celui du contenu. `inset-0` colle aux bords quoi qu'il
     * arrive, et le document ne peut plus déborder.
     */
    <div className="fixed inset-0 flex flex-col">
      <header className="shrink-0 border-b border-zinc-200 bg-white">
        <div className="mx-auto flex max-w-6xl flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3">
          <div className="flex items-center gap-2">
            <span className="grid size-8 place-items-center rounded-md bg-zinc-900 text-white">
              <Icon name="gauge" className="size-5" />
            </span>
            <span className="text-lg font-semibold text-zinc-900">Dashboard de livraisons</span>
          </div>

          <div className="ml-auto flex items-center gap-3">
            <ProjectPicker />
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

      {/* `id` plutôt qu'un contexte React : le verrou de défilement des modales
          doit désigner ce conteneur, la coquille est unique, et faire traverser
          une référence à travers tout l'arbre pour un singleton coûterait plus
          que ça ne clarifierait. */}
      <div
        id={APP_SCROLL_ID}
        className="min-h-0 flex-1 overflow-y-auto [scrollbar-gutter:stable]"
      >
        <main className="mx-auto max-w-6xl px-4 py-6">
          {/* La frontière est ici, et non par route : la coquille — en-tête,
              navigation, thème — reste affichée pendant le chargement d'un écran
              découpé. Une frontière plus haute ferait clignoter la navigation à
              chaque arrivée sur un écran paresseux.

              Le repli est un squelette et non un indicateur d'attente, par la
              même convention que les écrans (doc 08 §8.5) : il occupe la place
              du contenu attendu, ce qui évite le saut de mise en page. */}
          <Suspense fallback={<Skeleton className="h-64 w-full" />}>
            <Outlet />
          </Suspense>
        </main>
      </div>
    </div>
  )
}
