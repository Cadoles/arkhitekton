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
 * Une instance sans le plugin MSE n'a pas d'environnements : proposer l'onglet
 * mènerait à un écran dont chaque requête répond 404 (doc 16 §16.8).
 */

/** Les modules du projet courant, et ceux de l'instance — les mêmes par défaut. */
function identite(modules: string[], instance: string[] = modules) {
  return {
    subject: 's', username: 'vcarroy', email: 'v@example.fr', role: 'admin',
    projects: [{ slug: 'mse', label: 'MSE', modules }], modules: instance,
  }
}

describe('la navigation', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  it("n'affiche pas Environnements sans le module MSE", async () => {
    mockFetch([{ path: '/me', body: identite([]) }])
    render(<AppShell />)

    expect(await screen.findByRole('link', { name: /Releases/ })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /Environnements/ })).not.toBeInTheDocument()
  })

  // Allumé sur l'instance, éteint pour le projet courant : l'onglet suit le projet
  // (doc 16 §16.18).
  it("n'affiche pas Environnements quand le projet courant n'a pas le module", async () => {
    mockFetch([{ path: '/me', body: identite([], ['mse']) }])
    render(<AppShell />)

    expect(await screen.findByRole('link', { name: /Releases/ })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /Environnements/ })).not.toBeInTheDocument()
  })

  it('affiche Environnements quand le module MSE est allumé', async () => {
    mockFetch([{ path: '/me', body: identite(['mse']) }])
    render(<AppShell />)

    expect(await screen.findByRole('link', { name: /Environnements/ })).toBeInTheDocument()
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
            path="/environments"
            element={
              <ModuleRoute module="mse">
                <p>écran des environnements</p>
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

  it('mène aux releases sans le module MSE', async () => {
    naviguer('/', [])
    expect(await screen.findByText('écran des releases')).toBeInTheDocument()
  })

  it('mène aux environnements avec le module MSE', async () => {
    naviguer('/', ['mse'])
    expect(await screen.findByText('écran des environnements')).toBeInTheDocument()
  })

  it("renvoie une URL d'environnement vers les releases quand le module est éteint", async () => {
    naviguer('/environments', [])
    expect(await screen.findByText('écran des releases')).toBeInTheDocument()
    expect(screen.queryByText('écran des environnements')).not.toBeInTheDocument()
  })
})
