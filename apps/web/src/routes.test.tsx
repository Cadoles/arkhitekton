import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import { testQueryClient } from './test/render'
import { Outlet, RouterProvider, createMemoryRouter } from 'react-router'
import { buildRoutes } from './routes'
import { registerComponent } from './lib/components'
import { EMPTY_INTERFACE, type Screen } from './lib/interface'

vi.mock('./modules', () => ({ appModules: () => [], useActiveModules: () => [] }))

function Frame() {
  return (
    <div>
      cadre
      <Outlet />
    </div>
  )
}

registerComponent('test/frame', Frame)
registerComponent('test/text', ({ text }) => <p>{String(text)}</p>)

const screens: Screen[] = [
  { name: 'shell', path: '/', blocks: [{ id: 'core.shell', component: 'test/frame' }] },
  { name: 'home', parent: 'shell', blocks: [{ id: 'core.home', component: 'test/text', props: { text: 'accueil' } }] },
  { name: 'admin', path: 'admin', parent: 'shell', blocks: [{ id: 'core.admin', component: 'test/frame' }] },
  { name: 'users', path: 'users', parent: 'admin', blocks: [{ id: 'core.users', component: 'test/text', props: { text: 'utilisateurs' } }] },
  { name: 'former', path: 'people', parent: 'shell', redirect: '/admin/users' },
]

function at(path: string) {
  const router = createMemoryRouter(buildRoutes({ ...EMPTY_INTERFACE, screens }), { initialEntries: [path] })
  render(
    <QueryClientProvider client={testQueryClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

describe("les routes de l'interface", () => {
  it("rendent l'index sous son parent", async () => {
    at('/')
    expect(await screen.findByText('accueil')).toBeInTheDocument()
    expect(screen.getByText('cadre')).toBeInTheDocument()
  })

  it('emboîtent les écrans sous leurs parents', async () => {
    at('/admin/users')
    expect(await screen.findByText('utilisateurs')).toBeInTheDocument()
    expect(screen.getAllByText('cadre')).toHaveLength(2)
  })

  it('renvoient une ancienne adresse à la nouvelle', async () => {
    at('/people')
    expect(await screen.findByText('utilisateurs')).toBeInTheDocument()
  })
})
