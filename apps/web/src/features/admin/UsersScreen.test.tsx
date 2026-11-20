import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { render } from '../../test/render'
import { mockFetch } from '../../test/http'
import { UsersScreen } from './UsersScreen'
import type { Person } from './access'

/*
 * Utilisateurs : une ligne par personne, ce qu'elle peut faire et d'où ça vient,
 * réglé sur place ; les groupes de la forge ; ce que permet chaque rôle.
 */

const people: Person[] = [
  {
    subject: '150', username: 'newcomer', groups: [], last_seen: '2026-11-20T09:00:00Z', sessions: 0,
    roles: [], global: { role: '' }, projects: [], pending: true,
  },
  {
    subject: '121', username: 'vcarroy', groups: ['team:dev'], last_seen: '2026-11-20T09:00:00Z', sessions: 1,
    roles: [{ role: 'viewer', project: 'shop', project_label: 'Shop', granted_by: 'admin', granted_at: '2026-11-01T00:00:00Z' }],
    global: { role: 'releaser', source: 'team:dev' },
    projects: [{ project: 'shop', project_label: 'Shop', role: 'viewer' }],
    pending: false,
  },
]

const catalogue = [
  { role: 'viewer', permissions: ['read', 'release:export'], deploy: { open: false, sensitive: false } },
  { role: 'operator', permissions: ['read', 'release:export', 'pipeline:run'], deploy: { open: true, sensitive: false } },
  { role: 'releaser', permissions: ['read', 'pipeline:run', 'release:create:final'], deploy: { open: true, sensitive: true } },
  { role: 'admin', permissions: ['read', 'pipeline:run', 'release:create:final', 'admin'], deploy: { open: true, sensitive: true } },
]

function arm() {
  return mockFetch([
    { path: '/admin/people', body: { people } },
    { path: '/admin/groups/team', method: 'PUT', body: { group: 'team', role: 'operator' } },
    { path: '/admin/groups', body: { groups: [{ group: 'team:dev', members: 3, roles: [{ role: 'releaser', granted_by: 'admin', granted_at: '2026-11-01T00:00:00Z' }] }], known: [{ group: 'team', members: 4, roles: [] }, { group: 'team:dev', members: 3, roles: [] }] } },
    { path: '/admin/roles', method: 'POST', body: { subject: '150', role: 'operator' } },
    { path: '/admin/roles', body: { catalogue } },
    { path: '/projects', body: { projects: [{ slug: 'shop', label: 'Shop' }] } },
    { path: '/me', body: { subject: '121', username: 'vcarroy', role: 'admin', projects: [] } },
  ])
}

describe("l'onglet Utilisateurs", () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  it("montre une ligne par personne, ce qui s'applique et d'où ça vient", async () => {
    arm()
    render(<UsersScreen />)

    const waiting = (await screen.findByText('newcomer')).closest('li') as HTMLElement
    expect(within(waiting).getByText('En attente')).toBeInTheDocument()
    expect(screen.getByText(/1 personne\(s\) connectée\(s\) sans accès/)).toBeInTheDocument()

    const me = screen.getByText('vcarroy').closest('li') as HTMLElement
    expect(within(me).getByText('Responsable de livraison via team:dev')).toBeInTheDocument()
    expect(within(me).getByText('Shop : Lecteur')).toBeInTheDocument()
  })

  it('donne un rôle depuis la fiche de la personne', async () => {
    const calls = arm()
    render(<UsersScreen />)

    await userEvent.click(await screen.findByRole('button', { name: /newcomer/ }))
    await userEvent.selectOptions(screen.getByLabelText("Rôle propre : Toute l'instance"), 'operator')

    const sent = calls.find((call) => call.method === 'POST' && call.url.includes('/admin/roles'))
    expect(sent?.body).toEqual({ subject: '150', username: 'newcomer', role: 'operator' })
  })

  it('donne un rôle à un groupe vu aux connexions', async () => {
    const calls = arm()
    render(<UsersScreen />)

    expect(await screen.findByText('1 groupe(s) vu(s) aux connexions, sans rôle.')).toBeInTheDocument()
    await userEvent.type(screen.getByLabelText('Groupe'), 'team')
    await userEvent.selectOptions(screen.getByLabelText('Rôle du groupe'), 'operator')
    await userEvent.click(screen.getByRole('button', { name: 'Donner ce rôle' }))

    const sent = calls.find((call) => call.method === 'PUT')
    expect(sent?.url).toContain('/admin/groups/team/roles')
    expect(sent?.body).toEqual({ role: 'operator' })
  })

  it('dit ce que permet chaque rôle, promotions comprises', async () => {
    arm()
    render(<UsersScreen />)

    const row = (await screen.findByText('Promouvoir vers un environnement sensible')).closest('tr') as HTMLElement
    expect(within(row).getAllByText('oui')).toHaveLength(2)
    expect(within(row).getAllByText('non')).toHaveLength(2)
  })
})
