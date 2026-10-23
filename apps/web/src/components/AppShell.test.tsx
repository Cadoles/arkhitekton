import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import { render as baseRender } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { render, testQueryClient } from '../test/render'
import { mockFetch } from '../test/http'
import { AppShell } from './AppShell'
import { HomeRedirect, ModuleRoute } from './ModuleRoute'

/*
 * La navigation suit les modules allumés.
 *
 * Une instance sans un module n'a pas ses écrans : proposer son onglet mènerait à
 * un écran dont chaque requête répond 404. Le noyau n'embarque aucun module : un
 * module fictif, « widgets », tient le rôle de ceux qu'une distribution ajoute.
 */

vi.mock('../modules', async () => {
  const { projectModules, useSession } = await import('../features/auth/session')
  const widgets = {
    id: 'widgets',
    home: '/widgets',
    nav: [{ to: '/widgets', label: 'Widgets', icon: 'layers' as const }],
  }
  return {
    appModules: () => [widgets],
    moduleEventLabel: () => undefined,
    useActiveModules: () => {
      const session = useSession()
      if (session.kind !== 'authenticated') return []
      return [widgets].filter((module) => projectModules(session.identity).includes(module.id))
    },
  }
})

/** Les modules du projet courant, et ceux de l'instance — les mêmes par défaut. */
function identite(modules: string[], instance: string[] = modules) {
  return {
    subject: 's', username: 'vcarroy', email: 'v@example.fr', role: 'admin',
    projects: [{ slug: 'app', label: 'Application', modules }], modules: instance,
  }
}

describe('la navigation', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  it("n'affiche pas l'onglet d'un module éteint", async () => {
    mockFetch([{ path: '/me', body: identite([]) }])
    render(<AppShell />)

    expect(await screen.findByRole('link', { name: /Releases/ })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /Widgets/ })).not.toBeInTheDocument()
  })

  // Allumé sur l'instance, éteint pour le projet courant : l'onglet suit le projet.
  it("n'affiche pas l'onglet quand le projet courant n'a pas le module", async () => {
    mockFetch([{ path: '/me', body: identite([], ['widgets']) }])
    render(<AppShell />)

    expect(await screen.findByRole('link', { name: /Releases/ })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /Widgets/ })).not.toBeInTheDocument()
  })

  it("affiche l'onglet quand le module est allumé", async () => {
    mockFetch([{ path: '/me', body: identite(['widgets']) }])
    render(<AppShell />)

    expect(await screen.findByRole('link', { name: /Widgets/ })).toBeInTheDocument()
  })
})

function naviguer(chemin: string, modules: string[]) {
  mockFetch([{ path: '/me', body: identite(modules) }])
  return baseRender(
    <QueryClientProvider client={testQueryClient()}>
      <MemoryRouter initialEntries={[chemin]}>
        <Routes>
          <Route path="/" element={<HomeRedirect />} />
          <Route
            path="/widgets"
            element={
              <ModuleRoute module="widgets">
                <p>écran des widgets</p>
              </ModuleRoute>
            }
          />
          <Route path="/releases" element={<p>écran des releases</p>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe("la page d'accueil et les écrans d'un module", () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  it('mène aux releases sans module', async () => {
    naviguer('/', [])
    expect(await screen.findByText('écran des releases')).toBeInTheDocument()
  })

  it("mène à l'accueil du module allumé", async () => {
    naviguer('/', ['widgets'])
    expect(await screen.findByText('écran des widgets')).toBeInTheDocument()
  })

  it("renvoie l'URL d'un module éteint vers les releases", async () => {
    naviguer('/widgets', [])
    expect(await screen.findByText('écran des releases')).toBeInTheDocument()
    expect(screen.queryByText('écran des widgets')).not.toBeInTheDocument()
  })
})
