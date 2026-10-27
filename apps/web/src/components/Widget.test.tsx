import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router'
import { testQueryClient } from '../test/render'
import { mockFetch } from '../test/http'
import { ScreenView } from './Screen'
import { setProjects } from '../lib/project'
import { EMPTY_INTERFACE, InterfaceContext, type Screen, type UIInterface } from '../lib/interface'

/*
 * Un écran de plugin décrit sans React : un tableau lu sur une route du plugin,
 * ses pastilles, une action confirmée, et un widget composé.
 */

const deployment: Screen = {
  name: 'release',
  blocks: [
    {
      id: 'deploy.deployment',
      data: { deployment: { source: 'plugin', path: 'releases/{route.tag}/deployment', plugin: 'deploy', scope: 'project' } },
      widget: {
        card: {
          title: 'Déploiement de {route.tag}',
          children: [
            {
              table: {
                rows: 'deployment.environments',
                empty: 'Aucun environnement',
                columns: [
                  { label: 'Environnement', value: '{label}' },
                  { label: 'Version', value: "{deployed.tag | or: '-'}" },
                  { label: 'État', badge: { text: '{state}', tone: { from: 'state', map: { ok: 'success' }, default: 'danger' } } },
                  {
                    label: '',
                    when: [{ field: 'can_deploy', equals: true }],
                    action: {
                      label: 'Promouvoir',
                      method: 'POST',
                      path: 'releases/{route.tag}/promote/{id}',
                      plugin: 'deploy',
                      scope: 'project',
                      confirm: { text: 'Promouvoir {route.tag} vers {label} ?', challenge: '{id}' },
                      invalidates: ['deployment'],
                    },
                  },
                ],
              },
            },
            { when: [{ field: 'deployment.signoff', present: true }], use: { widget: 'deploy.signoff', with: { signoff: 'deployment.signoff' } } },
          ],
        },
      },
    },
  ],
}

const ui: UIInterface = {
  ...EMPTY_INTERFACE,
  widgets: { 'deploy.signoff': { params: { signoff: { required: true } }, widget: { text: { value: 'Validé par {signoff.by}' } } } },
}

const environments = {
  environments: [
    { id: 'staging', label: 'Recette', state: 'ok', can_deploy: true, deployed: { tag: '3.3.0' } },
    { id: 'prod', label: 'Production', state: 'drift', can_deploy: false },
  ],
  signoff: { by: 'alice' },
}

function show(page: Screen = deployment) {
  return render(
    <QueryClientProvider client={testQueryClient()}>
      <InterfaceContext.Provider value={ui}>
        <MemoryRouter initialEntries={['/releases/3.4.0']}>
          <Routes>
            <Route path="/releases/:tag" element={<ScreenView screen={page} />} />
          </Routes>
        </MemoryRouter>
      </InterfaceContext.Provider>
    </QueryClientProvider>,
  )
}

describe('les widgets déclaratifs', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
    setProjects([{ slug: 'app', label: 'App' }])
  })

  it('rendent les données de la route du plugin, ligne par ligne', async () => {
    mockFetch([{ path: '/projects/app/plugins/deploy/releases/3.4.0/deployment', body: environments }])
    show()

    const rows = await screen.findAllByRole('row')
    expect(rows).toHaveLength(3)
    const staging = within(rows[1] as HTMLElement)
    expect(staging.getByText('Recette')).toBeInTheDocument()
    expect(staging.getByText('3.3.0')).toBeInTheDocument()
    expect(staging.getByText('ok').className).toContain('healthy')
    expect(staging.getByRole('button', { name: 'Promouvoir' })).toBeInTheDocument()
    // Pas de droit sur la production : la donnée le dit, la cellule reste vide.
    const prod = within(rows[2] as HTMLElement)
    expect(prod.getByText('-')).toBeInTheDocument()
    expect(prod.getByText('drift').className).toContain('critical')
    expect(prod.queryByRole('button')).not.toBeInTheDocument()
    expect(screen.getByText('Validé par alice')).toBeInTheDocument()
  })

  it('confirment une action par son défi, appellent la route et relisent les données', async () => {
    const calls = mockFetch([
      { path: '/projects/app/plugins/deploy/releases/3.4.0/deployment', body: environments },
      { path: '/projects/app/plugins/deploy/releases/3.4.0/promote/staging', method: 'POST', body: {} },
    ])
    show()

    await userEvent.click(await screen.findByRole('button', { name: 'Promouvoir' }))
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText('Promouvoir 3.4.0 vers Recette ?')).toBeInTheDocument()
    const confirm = within(dialog).getByRole('button', { name: 'Promouvoir' })
    expect(confirm).toBeDisabled()
    await userEvent.type(within(dialog).getByRole('textbox'), 'staging')
    await userEvent.click(confirm)

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(calls.filter((call) => call.method === 'POST').map((call) => call.url)).toEqual([
      '/api/v1/projects/app/plugins/deploy/releases/3.4.0/promote/staging',
    ])
    await waitFor(() => expect(calls.filter((call) => call.url.endsWith('/deployment'))).toHaveLength(2))
  })

  it('envoient le formulaire d’une action', async () => {
    const calls = mockFetch([{ path: '/plugins/deploy/notes', method: 'PUT', body: {} }])
    show({
      name: 'notes',
      blocks: [
        {
          id: 'deploy.notes',
          widget: {
            action: {
              label: 'Noter',
              method: 'PUT',
              path: 'notes',
              plugin: 'deploy',
              scope: 'instance',
              form: [
                { name: 'text', label: 'Texte', required: true },
                { name: 'level', label: 'Niveau', type: 'select', oneOf: ['bas', 'haut'] },
              ],
            },
          },
        },
      ],
    })

    await userEvent.click(screen.getByRole('button', { name: 'Noter' }))
    const submit = within(screen.getByRole('dialog')).getByRole('button', { name: 'Noter' })
    expect(submit).toBeDisabled()
    await userEvent.type(screen.getByLabelText(/Texte/), 'bonjour')
    await userEvent.selectOptions(screen.getByLabelText('Niveau'), 'haut')
    await userEvent.click(submit)

    await waitFor(() => expect(calls).toHaveLength(1))
    expect(calls[0]).toEqual({ url: '/api/v1/plugins/deploy/notes', method: 'PUT', body: { text: 'bonjour', level: 'haut' } })
  })

  it('disent quand il n’y a rien, et se taisent quand une condition manque', async () => {
    mockFetch([{ path: '/deployment', body: { environments: [] } }])
    show()
    expect(await screen.findByText('Aucun environnement')).toBeInTheDocument()
    expect(screen.queryByText(/Validé par/)).not.toBeInTheDocument()
  })
})
