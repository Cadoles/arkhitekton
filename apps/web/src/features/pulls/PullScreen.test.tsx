import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { setProjects } from '../../lib/project'
import { mockFetch } from '../../test/http'
import { render } from '../../test/render'
import { PullScreen } from './PullScreen'

vi.mock('react-router', async () => {
  const actual = await vi.importActual<Record<string, unknown>>('react-router')
  return { ...actual, useParams: () => ({ number: '12' }) }
})

const pull = {
  pull: {
    number: 12, title: 'Ajoute la recherche', author: 'dev', source_branch: 'feature', target_branch: 'main',
    head_sha: 'abcdef0123456789', state: 'open', tag: 'pr-12-abcdef0',
  },
  jobs: [{ id: 3, kind: 'docker/build', label: 'Construction', state: 'succeeded', requested_by: 'system:plan', requested_at: '2026-11-02T10:00:00Z' }],
  plan_runs: [],
}

const queue = {
  queues: [],
  kinds: [
    {
      kind: 'docker/build', label: 'Construction',
      inputs: [
        { name: 'revision', required: true, suggest: 'git-ref' },
        { name: 'tag-name', required: true, suggest: 'release-tag' },
        { name: 'image-name', required: true, suggest: 'image-name' },
        { name: 'portal-image', required: true, suggest: 'image:portal' },
        { name: 'number', required: true, suggest: 'pull-request' },
      ],
    },
  ],
  suggestions: {},
  max_concurrent: 4,
}

const notReviewed = { reviewed: false, findings: [], attachments: [] }

function arm(review: object = notReviewed) {
  return mockFetch([
    { path: '/me', body: { subject: 's', username: 'admin', role: 'admin', projects: [{ slug: 'shop', label: 'Shop' }] } },
    { path: '/pulls/12/review', body: review },
    { path: '/pulls/12/tests', body: { results: [], score: { value: 0, measured: false, health: 'na', signals: [], redistributed: [] } } },
    { path: '/pulls/12/launch', method: 'POST', status: 202, body: { id: 4, kind: 'docker/build', params: {} } },
    { path: '/pulls/12', body: pull },
    { path: '/queue', body: queue },
  ])
}

describe("la page d'une pull request", () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
    setProjects([{ slug: 'shop', label: 'Shop' }])
  })

  it('montre la PR, ce qui a tourné, et lit ses résultats à elle', async () => {
    const calls = arm()
    render(<PullScreen />)

    expect(await screen.findByText('Ajoute la recherche')).toBeInTheDocument()
    expect(screen.getByText('pr-12-abcdef0')).toBeInTheDocument()
    expect(screen.getByText('Construction')).toBeInTheDocument()
    await waitFor(() => expect(calls.some((call) => call.url.includes('/projects/shop/pulls/12/tests'))).toBe(true))
    // Rien de lancé qui produise des résultats : la section n'apparaît pas.
    expect(screen.queryByText(/Rien n'a été mesuré/)).not.toBeInTheDocument()
  })

  it('ajoute une case par pipeline lancé, hors score quand il ne pèse rien', async () => {
    mockFetch([
      { path: '/me', body: { subject: 's', username: 'admin', role: 'admin', projects: [{ slug: 'shop', label: 'Shop' }] } },
      { path: '/pulls/12/review', body: notReviewed },
      {
        path: '/pulls/12/tests',
        body: {
          results: [],
          score: {
            value: 100, measured: true, health: 'healthy', redistributed: [],
            signals: [
              { key: 'pipeline:php/unit-tests', label: 'Tests unitaires', signal: 'unit', ref: 'php/unit-tests', group: 'unit',
                shape: 'cases', value: 100, weight: 100, declared: 25, measured: true, detail: '7/7 passed', health: 'healthy' },
              { key: 'pipeline:mse/rgaa-audit', label: 'Audit RGAA', signal: '', ref: 'mse/rgaa-audit', group: 'accessibility',
                shape: 'cases', value: 80, weight: 0, declared: 0, measured: true, detail: '8/10 passed', health: 'warn' },
            ],
          },
        },
      },
      { path: '/pulls/12', body: pull },
    ])
    render(<PullScreen />)

    expect(await screen.findByText('Audit RGAA')).toBeInTheDocument()
    expect(screen.getByText('hors score')).toBeInTheDocument()
    expect(screen.getByText('poids 100 %')).toBeInTheDocument()
  })

  it("lance un pipeline en ne demandant que ce que la tête ne remplit pas", async () => {
    const calls = arm()
    render(<PullScreen />)

    await userEvent.click(await screen.findByRole('button', { name: /Lancer un pipeline/ }))
    expect(await screen.findByText('image-name')).toBeInTheDocument()
    expect(screen.queryByText('revision')).not.toBeInTheDocument()
    expect(screen.queryByText('tag-name')).not.toBeInTheDocument()
    expect(screen.queryByText('portal-image')).not.toBeInTheDocument()
    expect(screen.queryByText('number')).not.toBeInTheDocument()

    await userEvent.type(screen.getByLabelText(/image-name/), 'portal')
    await userEvent.click(screen.getByRole('button', { name: 'Lancer' }))

    const sent = calls.find((call) => call.method === 'POST' && call.url.includes('/pulls/12/launch'))
    expect(sent?.body).toEqual({ kind: 'docker/build', params: { 'image-name': 'portal' } })
  })

  it("montre la revue IA à part, avec ce qui sort du périmètre replié", async () => {
    arm({
      reviewed: true,
      status: 'fail',
      summary: 'Le jeton est écrit dans les journaux.',
      reviewed_at: '2026-11-17T10:00:00Z',
      attributes: { verdict: 'request_changes', reviewers: '2/3', head: 'abcdef0123456789' },
      findings: [
        { fingerprint: 'a', severity: 'HIGH', title: 'Le jeton fuit', tool: 'pi-qwen', category: 'review', rule_id: 'security',
          location: 'internal/http/client.go:42-44', fixable: false, status: 'open', description: 'Retirer l\'en-tête.', suppressed: false },
        { fingerprint: 'b', severity: 'LOW', title: 'Code mort voisin', tool: 'pi-mistral', category: 'review', rule_id: 'maintainability',
          location: 'internal/legacy/old.go:3-9', fixable: false, status: 'open', suppressed: true, suppression_reason: 'hors du périmètre de la pull request' },
      ],
      attachments: [{ id: 8, name: 'review.md', media_type: 'text/markdown', size_bytes: 2048 }],
    })
    render(<PullScreen />)

    expect(await screen.findByText('Revue IA')).toBeInTheDocument()
    expect(screen.getByText('Modifications demandées')).toBeInTheDocument()
    expect(screen.getByText('internal/http/client.go:42-44')).toBeInTheDocument()
    expect(screen.getByText(/1 remarque\(s\) hors du périmètre/)).toBeInTheDocument()
    expect(screen.getByText('review.md')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: 'Détail' }))
    expect(screen.getByText("Retirer l'en-tête.")).toBeInTheDocument()
  })

  it("dit pourquoi un job a échoué", async () => {
    const failing = {
      ...pull,
      jobs: [
        ...pull.jobs,
        { id: 4, kind: 'review/pull-request', label: 'Revue IA', state: 'failed', requested_by: 'vcarroy',
          requested_at: '2026-11-17T10:00:00Z', error: "the project has no active deploy key" },
      ],
    }
    mockFetch([
      { path: '/me', body: { subject: 's', username: 'admin', role: 'admin', projects: [{ slug: 'shop', label: 'Shop' }] } },
      { path: '/pulls/12/review', body: notReviewed },
      { path: '/pulls/12/tests', body: { results: [], score: { value: 0, measured: false, health: 'na', signals: [], redistributed: [] } } },
      { path: '/pulls/12', body: failing },
    ])
    render(<PullScreen />)

    expect(await screen.findByText('the project has no active deploy key')).toBeInTheDocument()
  })

  it("ne montre rien de la revue tant qu'aucune n'a été faite", async () => {
    const calls = arm()
    render(<PullScreen />)

    expect(await screen.findByText('Ajoute la recherche')).toBeInTheDocument()
    await waitFor(() => expect(calls.some((call) => call.url.includes('/pulls/12/review'))).toBe(true))
    expect(screen.queryByText('Revue IA')).not.toBeInTheDocument()
  })
})
