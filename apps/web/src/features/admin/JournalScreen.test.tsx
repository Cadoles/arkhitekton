import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render as baseRender, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router'
import { render, testQueryClient } from '../../test/render'
import { mockFetch } from '../../test/http'
import { JournalScreen } from './JournalScreen'
import type { JournalAutomation, JournalEntry } from './journal'

/*
 * Le journal : actions, événements et automatisations dans un seul fil.
 *
 * Ce que ces tests tiennent : les natures se distinguent, un événement dit ce
 * qui y a réagi (ou que rien n'y a réagi), une automatisation montre ses étapes
 * et le motif d'un échec, et un type inconnu reste lisible : un journal qui
 * cache des lignes fait croire qu'il ne s'est rien passé.
 */

function run(overrides: Partial<JournalAutomation>): JournalAutomation {
  return {
    id: 1, trigger: 'builtin:release', event_key: 'release.created:42', state: 'succeeded',
    created_at: '2026-11-20T10:04:00Z', context: {}, stages: [], ...overrides,
  }
}

const failedReaction = run({
  id: 3, trigger: 'reaction:3', name: 'Vérifier la RC', state: 'failed', context: { tag: '1.8.0-rc1' },
  stages: [
    {
      label: 'Vérifications',
      steps: [
        { stage: 0, position: 0, kind: 'pipeline', ref: 'unit-tests', state: 'succeeded', job_id: 1381 },
        { stage: 0, position: 1, kind: 'action', ref: 'webhook', label: 'Prévenir', state: 'failed', detail: 'le destinataire a répondu 401: jeton expiré' },
      ],
    },
  ],
})

const entries: JournalEntry[] = [
  {
    nature: 'action', id: 1, at: '2026-11-20T10:05:00Z', project: 'mse',
    actor: 'vcarroy', action: 'release.create', target: '1.8.0-rc1', payload: { kind: 'pre' },
  },
  {
    nature: 'event', id: 2, at: '2026-11-20T10:04:00Z', project: 'mse', type: 'release.created', subject: '1.8.0-rc1',
    payload: { tag: '1.8.0-rc1' },
    automations: [failedReaction, run({ id: 4 })],
  },
  { nature: 'event', id: 3, at: '2026-11-20T10:03:00Z', project: 'mse', type: 'gitea.push', subject: 'main', source: 'gitea' },
  { nature: 'event', id: 4, at: '2026-11-20T10:02:00Z', type: 'release.built', subject: '1.7.0', payload: { outcome: 'failed' } },
  { nature: 'event', id: 5, at: '2026-11-20T10:01:00Z', type: 'gitea.pull_request.opened', pending: true },
  {
    nature: 'automation', id: 6, at: '2026-11-20T03:00:00Z', project: 'mse',
    automations: [run({ id: 6, trigger: 'schedule:2', name: 'Tests de nuit', event_key: 'schedule:2:2026-11-20T03:00:00Z' })],
  },
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

  it("déplie une automatisation sur ses étapes, et dit ce qui a échoué", async () => {
    arm()
    render(<JournalScreen />)
    const chip = await screen.findByRole('button', { name: /Vérifier la RC · échouée/ })
    expect(screen.queryByText(/401: jeton expiré/)).not.toBeInTheDocument()

    await userEvent.click(chip)
    expect(chip).toHaveAttribute('aria-expanded', 'true')
    // Le motif, sans quoi « échoué » n'apprend rien et oblige à relancer pour voir.
    expect(screen.getByText(/401: jeton expiré/)).toBeInTheDocument()
    expect(screen.getByText('Vérifications')).toBeInTheDocument()
    // Le geste de pipeline mène à son job : c'est là que vivent les journaux.
    expect(screen.getByText('job n° 1381')).toBeInTheDocument()

    await userEvent.click(chip)
    expect(screen.queryByText(/401: jeton expiré/)).not.toBeInTheDocument()
  })

  it("montre seule une planification, qu'aucun événement n'a déclenchée", async () => {
    arm()
    render(<JournalScreen />)
    expect(await screen.findByText('planifiée')).toBeInTheDocument()
    const line = row('planifiée')
    expect(line.getByText('Automatisation')).toBeInTheDocument()
    expect(line.getByRole('button', { name: /Tests de nuit · réussie/ })).toBeInTheDocument()
  })

  it("s'ouvre filtré quand on arrive de l'ancien onglet des automatisations", async () => {
    const calls = arm()
    baseRender(
      <QueryClientProvider client={testQueryClient()}>
        <MemoryRouter initialEntries={['/admin/journal?nature=automation']}>
          <JournalScreen />
        </MemoryRouter>
      </QueryClientProvider>,
    )
    await screen.findByText('Release lancée')
    expect(screen.getByLabelText('Nature des entrées')).toHaveValue('automation')
    expect(calls.find((call) => call.url.includes('/admin/journal'))?.url).toContain('nature=automation')
  })
})
