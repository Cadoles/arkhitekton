import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { render } from '../../test/render'
import { mockFetch } from '../../test/http'
import { selectProject, setProjects } from '../../lib/project'
import { StatsScreen, duration } from './StatsScreen'
import type { Stats, StatsBucket } from './api'

/*
 * Les statistiques : ce qui a été livré, et comment les pipelines se comportent.
 *
 * Ce que ces tests tiennent : les comptes et les barres se lisent, un projet sans historique le
 * dit, et « Tous mes projets » interroge sans projet.
 */

const weeks = ['2026-10-19', '2026-10-26', '2026-11-02', '2026-11-09', '2026-11-16']

function stats(overrides: Partial<Stats> = {}): Stats {
  return {
    range: '30d',
    bucket: 'week',
    since: '2026-10-19T00:00:00Z',
    projects: [{ slug: 'mse', label: 'MSE' }],
    releases: {
      total: 4,
      by_state: [
        { key: 'failed', count: 2 },
        { key: 'released', count: 2 },
      ],
      by_kind: [{ key: 'pre', label: 'Pré-release', count: 4 }],
      buckets: weeks.map((week, index): StatsBucket => {
        const by_state: Record<string, number> = index === 4 ? { failed: 2, released: 1 } : index === 2 ? { released: 1 } : {}
        return { start: `${week}T00:00:00Z`, count: index === 4 ? 3 : index === 2 ? 1 : 0, by_state }
      }),
    },
    pipelines: [
      {
        kind: 'docker/build',
        label: "Construction d'image",
        runs: 10,
        succeeded: 8,
        failed: 2,
        cancelled: 0,
        median_seconds: 72,
      },
      { kind: 'php/unit-tests', label: 'Tests unitaires', runs: 3, succeeded: 3, failed: 0, cancelled: 0 },
    ],
    ...overrides,
  }
}

function arm(body: Stats) {
  setProjects([{ slug: 'mse', label: 'MSE' }])
  selectProject('mse')
  return mockFetch([{ path: '/stats', body }])
}

describe('les statistiques', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  it('compte les releases lancées, par état, par type et par semaine', async () => {
    arm(stats())
    render(<StatsScreen />)

    expect(await screen.findByText('4', { selector: 'p' })).toBeInTheDocument()
    const byState = screen.getByLabelText('Par état')
    expect(within(byState).getByText('Échec')).toBeInTheDocument()
    expect(within(byState).getByText('Validée')).toBeInTheDocument()
    expect(screen.getByText(/Pré-release/)).toBeInTheDocument()
    const bars = within(screen.getByLabelText('Par semaine')).getAllByRole('listitem')
    expect(bars).toHaveLength(5)
    expect(bars[4]).toHaveAttribute('title', 'Semaine du 16 nov. : 3')
  })

  it('dit pour chaque pipeline ses exécutions, ses échecs et sa durée médiane', async () => {
    arm(stats())
    render(<StatsScreen />)

    const row = (await screen.findByText("Construction d'image")).closest('tr') as HTMLElement
    expect(within(row).getByText('10')).toBeInTheDocument()
    expect(within(row).getByText('2 (20 %)')).toBeInTheDocument()
    expect(within(row).getByText('1 min 12 s')).toBeInTheDocument()
    const other = screen.getByText('Tests unitaires').closest('tr') as HTMLElement
    expect(within(other).getByText('non mesurée')).toBeInTheDocument()
  })

  it("dit qu'un projet sans historique n'a rien lancé", async () => {
    const empty = stats()
    arm({
      ...empty,
      releases: {
        total: 0,
        by_state: [],
        by_kind: [],
        buckets: empty.releases.buckets.map((entry) => ({ ...entry, count: 0, by_state: {} })),
      },
      pipelines: [],
    })
    render(<StatsScreen />)

    expect(await screen.findByText('Aucune release lancée sur cette période.')).toBeInTheDocument()
    expect(screen.getByText('Aucun pipeline terminé sur cette période.')).toBeInTheDocument()
  })

  it('interroge le projet courant, puis tous les projets, sur la période choisie', async () => {
    const calls = arm(stats())
    render(<StatsScreen />)
    await screen.findByText("Construction d'image")

    await userEvent.click(screen.getByRole('button', { name: 'Tous mes projets' }))
    await userEvent.click(screen.getByRole('button', { name: '12 mois' }))

    const urls = calls.map((call) => call.url).filter((url) => url.includes('/stats'))
    expect(urls[0]).toContain('project=mse')
    expect(urls.at(-1)).toContain('range=12m')
    expect(urls.at(-1)).not.toContain('project=')
  })
})

describe('une durée', () => {
  it('se lit en secondes, minutes ou heures', () => {
    expect(duration(45)).toBe('45 s')
    expect(duration(192)).toBe('3 min 12 s')
    expect(duration(3900)).toBe('1 h 05 min')
  })
})
