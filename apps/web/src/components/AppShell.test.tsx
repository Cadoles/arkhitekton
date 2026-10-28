import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render as baseRender, screen } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router'
import { testQueryClient } from '../test/render'
import { mockFetch } from '../test/http'
import { AppShell } from './AppShell'
import { HomeRedirect } from './HomeRedirect'
import { EMPTY_INTERFACE, InterfaceContext, type UIInterface } from '../lib/interface'

/*
 * Le menu principal et l'accueil suivent l'interface composée par le serveur :
 * un plugin éteint pour le projet n'y a plus d'entrée, le serveur l'a retirée.
 */

const identity = {
  subject: 's', username: 'vcarroy', email: 'v@example.fr', role: 'admin',
  projects: [{ slug: 'app', label: 'Application' }],
}

function withMain(main: UIInterface['navigations'][string]): UIInterface {
  return { ...EMPTY_INTERFACE, navigations: { main } }
}

function show(ui: UIInterface, path: string) {
  mockFetch([{ path: '/me', body: identity }])
  return baseRender(
    <QueryClientProvider client={testQueryClient()}>
      <InterfaceContext.Provider value={ui}>
        <MemoryRouter initialEntries={[path]}>
          <Routes>
            <Route path="/" element={<HomeRedirect />} />
            <Route path="/environments" element={<AppShell />} />
            <Route path="/releases" element={<p>écran des releases</p>} />
          </Routes>
        </MemoryRouter>
      </InterfaceContext.Provider>
    </QueryClientProvider>,
  )
}

describe('le menu principal', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  it("suit l'interface composée, et l'accueil qu'un plugin y fixe", async () => {
    show(
      withMain({
        home: '/environments',
        items: [
          { id: 'mse.environments', label: 'Environnements', icon: 'server', to: '/environments' },
          { id: 'core.releases', label: 'Releases', icon: 'doc', to: '/releases' },
        ],
      }),
      '/',
    )
    const links = await screen.findAllByRole('link')
    expect(links.map((link) => link.textContent)).toEqual(['Environnements', 'Releases'])
  })

  it('mène aux releases quand aucun plugin ne fixe l’accueil', async () => {
    show(withMain({ items: [] }), '/')
    expect(await screen.findByText('écran des releases')).toBeInTheDocument()
  })
})
