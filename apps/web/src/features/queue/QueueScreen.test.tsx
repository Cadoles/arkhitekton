import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { render } from '../../test/render'
import { mockFetch } from '../../test/http'
import { QueueScreen } from './QueueScreen'
import type { Job, Queue } from './api'

/*
 * La file d'attente.
 *
 * Ce que ces tests tiennent : une seule liste dans l'ordre du service, où chaque
 * job dit sur quoi il porte et ce qui le retient ; les files vides se replient
 * au lieu d'empiler des cartes vides.
 */

function queue(kind: string, label: string, overrides: Partial<Queue> = {}): Queue {
  return {
    kind,
    label,
    max_concurrent: 5,
    default_max_concurrent: 5,
    overridden: false,
    known: true,
    running: 0,
    waiting: 0,
    ...overrides,
  }
}

const queues: Queue[] = [
  queue('build', "Construction d'image", { running: 1 }),
  queue('e2e', 'Tests E2E', { max_concurrent: 1, default_max_concurrent: 1, running: 1, waiting: 2 }),
  queue('audit', 'Audit de sécurité'),
  queue('scan', "Scan d'image"),
]

const jobs: Job[] = [
  {
    id: 1,
    kind: 'build',
    label: "Construction d'image",
    priority: 100,
    state: 'running',
    attempt: 1,
    requested_by: 'vcarroy',
    requested_at: '2026-11-20T10:00:00Z',
    started_at: '2026-11-20T10:00:05Z',
    run_name: 'build-abc',
    project: 'shop',
    release_tag: '3.3.0-rc7',
  },
  {
    id: 2,
    kind: 'e2e',
    label: 'Tests E2E',
    priority: 100,
    state: 'running',
    attempt: 1,
    requested_by: 'vcarroy',
    requested_at: '2026-11-20T10:00:00Z',
    run_name: 'e2e-abc',
    project: 'store',
    pull_request: 3,
  },
  {
    id: 3,
    kind: 'e2e',
    label: 'Tests E2E',
    priority: 100,
    state: 'queued',
    attempt: 1,
    queue_reason: 'kind-busy',
    requested_by: 'vcarroy',
    requested_at: '2026-11-20T10:01:00Z',
    project: 'shop',
    pull_request: 4,
  },
  {
    id: 4,
    kind: 'e2e',
    label: 'Tests E2E',
    priority: 100,
    state: 'queued',
    attempt: 2,
    queue_reason: 'kind-busy',
    requested_by: 'vcarroy',
    requested_at: '2026-11-20T10:02:00Z',
    project: 'store',
    release_tag: '1.2.0',
  },
]

function arm(body: { queues: Queue[]; jobs: Job[] }, role = 'operator') {
  return mockFetch([
    { path: '/me', body: { subject: '121', username: 'vcarroy', role, projects: [] } },
    { path: '/queue', body: { ...body, kinds: [], suggestions: {}, max_concurrent: 10 } },
  ])
}

function row(text: string) {
  return within(screen.getByText(text).closest('li') as HTMLElement)
}

describe("la file d'attente", () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  it('liste les jobs en cours puis en attente, chacun avec ce sur quoi il porte', async () => {
    arm({ queues, jobs })
    render(<QueueScreen />)

    expect(await screen.findByText('3.3.0-rc7')).toBeInTheDocument()
    const summary = (_: string, element: Element | null) =>
      element?.tagName === 'P' && element.textContent === '2 en cours sur 10 au total · 2 en attente'
    expect(screen.getByText(summary)).toBeInTheDocument()

    const runningList = screen.getByText(/^En cours/).closest('section') as HTMLElement
    expect(within(runningList).getAllByRole('listitem')).toHaveLength(2)
    expect(row('3.3.0-rc7').getByText('shop')).toBeInTheDocument()
    expect(row('3.3.0-rc7').getByRole('link', { name: 'build-abc' })).toBeInTheDocument()

    // L'attente dit ce qui la retient, et le rang est celui dans sa file.
    const waitingItems = within(screen.getByText(/^En attente/).closest('section') as HTMLElement).getAllByRole(
      'listitem',
    )
    expect(waitingItems).toHaveLength(2)
    const [first, second] = waitingItems as [HTMLElement, HTMLElement]
    expect(within(first).getByText('PR #4')).toBeInTheDocument()
    expect(within(first).getByText('1')).toBeInTheDocument()
    expect(within(first).getByText(/La file Tests E2E est pleine/)).toBeInTheDocument()
    expect(within(second).getByText('2')).toBeInTheDocument()
    expect(within(second).getByText('essai 2')).toBeInTheDocument()
    expect(within(first).getByRole('button', { name: 'Annuler' })).toBeInTheDocument()
  })

  it('montre les files occupées et replie les vides', async () => {
    arm({ queues, jobs })
    render(<QueueScreen />)

    expect(await screen.findByText('pleine')).toBeInTheDocument()
    expect(screen.queryByText('Audit de sécurité')).not.toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: /2 autres files vides/ }))
    expect(screen.getByText('Audit de sécurité')).toBeInTheDocument()
    expect(screen.getByText("Scan d'image")).toBeInTheDocument()
  })

  it('dit quand rien ne tourne, sans empiler de files vides', async () => {
    arm({ queues: queues.map((entry) => ({ ...entry, running: 0, waiting: 0 })), jobs: [] }, 'reader')
    render(<QueueScreen />)

    expect(await screen.findByText(/Rien ne tourne ni n.attend/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /4 files, toutes vides/ })).toBeInTheDocument()
    expect(screen.queryByText("Construction d'image")).not.toBeInTheDocument()
  })
})
