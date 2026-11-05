import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
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
      ],
    },
  ],
  suggestions: {},
  max_concurrent: 4,
}

function arm() {
  return mockFetch([
    { path: '/me', body: { subject: 's', username: 'admin', role: 'admin', projects: [{ slug: 'shop', label: 'Shop' }] } },
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
    await screen.findByText(/Rien n'a été mesuré/)
    expect(calls.some((call) => call.url.includes('/projects/shop/pulls/12/tests'))).toBe(true)
  })

  it("lance un pipeline en ne demandant que ce que la tête ne remplit pas", async () => {
    const calls = arm()
    render(<PullScreen />)

    await userEvent.click(await screen.findByRole('button', { name: /Lancer un pipeline/ }))
    expect(await screen.findByText('image-name')).toBeInTheDocument()
    expect(screen.queryByText('revision')).not.toBeInTheDocument()
    expect(screen.queryByText('tag-name')).not.toBeInTheDocument()
    expect(screen.queryByText('portal-image')).not.toBeInTheDocument()

    await userEvent.type(screen.getByLabelText(/image-name/), 'portal')
    await userEvent.click(screen.getByRole('button', { name: 'Lancer' }))

    const sent = calls.find((call) => call.method === 'POST' && call.url.includes('/pulls/12/launch'))
    expect(sent?.body).toEqual({ kind: 'docker/build', params: { 'image-name': 'portal' } })
  })
})
