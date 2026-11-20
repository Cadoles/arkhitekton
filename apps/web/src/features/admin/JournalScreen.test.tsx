import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { render } from '../../test/render'
import { mockFetch } from '../../test/http'
import { JournalScreen } from './JournalScreen'
import type { JournalEntry } from './journal'

/*
 * Le journal : actions et événements dans un seul fil.
 *
 * Ce que ces tests tiennent : les deux natures se distinguent, un événement dit
 * ce qui y a réagi (ou que rien n'y a réagi), et un type inconnu reste lisible —
 * un journal qui cache des lignes fait croire qu'il ne s'est rien passé.
 */

const entries: JournalEntry[] = [
  {
    nature: 'action', id: 1, at: '2026-11-20T10:05:00Z', project: 'mse',
    actor: 'vcarroy', action: 'release.create', target: '1.8.0-rc1', payload: { kind: 'pre' },
  },
  {
    nature: 'event', id: 2, at: '2026-11-20T10:04:00Z', project: 'mse', type: 'release.created', subject: '1.8.0-rc1',
    payload: { tag: '1.8.0-rc1' },
    automations: [{ trigger: 'reaction:3', name: 'Vérifier la RC', state: 'failed' }, { trigger: 'builtin:release', state: 'succeeded' }],
  },
  { nature: 'event', id: 3, at: '2026-11-20T10:03:00Z', project: 'mse', type: 'gitea.push', subject: 'main', source: 'gitea' },
  { nature: 'event', id: 4, at: '2026-11-20T10:02:00Z', type: 'release.built', subject: '1.7.0', payload: { outcome: 'failed' } },
  { nature: 'event', id: 5, at: '2026-11-20T10:01:00Z', type: 'gitea.pull_request.opened', pending: true },
]

function arm() {
  return mockFetch([
    { path: '/admin/journal', body: { entries } },
    { path: '/projects', body: { projects: [{ slug: 'mse', label: 'MSE' }, { slug: 'shop', label: 'Shop' }] } },
  ])
}

function row(text: string) {
  return within(screen.getByText(text).closest('li') as HTMLElement)
}

describe("le journal de l'application", () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  it('mêle actions et événements, chacun avec ce qui le distingue', async () => {
    arm()
    render(<JournalScreen />)

    expect(await screen.findByText('Création de brouillon de release')).toBeInTheDocument()
    expect(row('Création de brouillon de release').getByText('Action')).toBeInTheDocument()
    expect(row('Création de brouillon de release').getByText('par vcarroy')).toBeInTheDocument()

    const created = row('Release lancée')
    expect(created.getByText('Événement')).toBeInTheDocument()
    expect(created.getByText(/Vérifier la RC · échouée/)).toBeInTheDocument()
    expect(created.getByText(/Automatisation de release · réussie/)).toBeInTheDocument()

    // Ce que personne n'a nommé reste lisible, et dit que rien n'y a réagi.
    expect(row('gitea.push').getByText(/Aucune automatisation n.a réagi/)).toBeInTheDocument()
    expect(row('gitea.push').getByText('via gitea')).toBeInTheDocument()
    // Un fait que plus personne n'émet garde son libellé et son issue.
    expect(row('Construction conclue').getByText('échoué')).toBeInTheDocument()
    expect(row('gitea.pull_request.opened').getByText(/En attente de routage/)).toBeInTheDocument()
  })

  it('filtre par nature, projet et recherche', async () => {
    const calls = arm()
    render(<JournalScreen />)
    await screen.findByText('Release lancée')

    await userEvent.selectOptions(screen.getByLabelText('Nature des entrées'), 'event')
    await userEvent.selectOptions(screen.getByLabelText('Projet'), 'shop')
    await userEvent.type(screen.getByLabelText('Rechercher dans le journal'), '1.8')
    await userEvent.click(screen.getByRole('button', { name: 'Rechercher' }))

    const last = calls.filter((call) => call.url.includes('/admin/journal')).at(-1)
    expect(last?.url).toContain('nature=event')
    expect(last?.url).toContain('project=shop')
    expect(last?.url).toContain('q=1.8')
  })
})
