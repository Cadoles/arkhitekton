import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it } from 'vitest'
import { DraftAction } from './ReleaseScreen'
import { selectProject, setProjects } from '../../lib/project'
import { mockFetch, type Call } from '../../test/http'
import { render } from '../../test/render'
import type { ReleaseDetail } from './api'

/*
 * Un brouillon : ce que ses gestes envoient. Le lancement est le seul qui déclenche
 * la construction et les automatisations, et il ne part qu'une fois le tag ressaisi.
 */

const draft: ReleaseDetail = {
  tag: '3.2.0-rc1',
  kind: 'pre',
  state: 'draft',
  image_version: '3.2.0-rc1',
  git_ref: 'main',
  created_by: 'vcarroy',
  created_at: '2026-11-03T10:00:00Z',
}

function setup(): Call[] {
  setProjects([{ slug: 'app', label: 'App' }])
  selectProject('app')
  return mockFetch([
    { path: '/releases/3.2.0-rc1/launch', method: 'POST', body: { release: { ...draft, state: 'building' } } },
    { path: '/releases/3.2.0-rc1/draft', method: 'PUT', body: { release: { ...draft, git_ref: 'release/3.2' } } },
    { path: '/releases/3.2.0-rc1', method: 'DELETE', status: 204 },
  ])
}

describe('DraftAction', () => {
  beforeEach(() => {
    setProjects([])
  })

  it('lance le brouillon une fois le tag ressaisi', async () => {
    const calls = setup()
    const user = userEvent.setup()
    render(<DraftAction release={draft} />)

    await user.click(screen.getByRole('button', { name: 'Lancer la release' }))
    const confirm = screen.getByRole('button', { name: 'Lancer' })
    expect(confirm).toBeDisabled()
    await user.type(screen.getByLabelText(/pour confirmer/), '3.2.0-rc1')
    await user.click(confirm)

    await waitFor(() => {
      expect(calls.some((call) => call.method === 'POST' && call.url.endsWith('/releases/3.2.0-rc1/launch'))).toBe(true)
    })
  })

  it("enregistre la référence avant de laisser lancer", async () => {
    const calls = setup()
    const user = userEvent.setup()
    render(<DraftAction release={draft} />)

    const field = screen.getByLabelText('Référence Git à construire')
    await user.clear(field)
    await user.type(field, 'release/3.2')
    expect(screen.getByRole('button', { name: 'Lancer la release' })).toBeDisabled()

    await user.click(screen.getByRole('button', { name: 'Enregistrer' }))
    await waitFor(() => {
      expect(calls.find((call) => call.method === 'PUT')?.body).toEqual({ git_ref: 'release/3.2' })
    })
  })

  it("ne propose pas de référence pour un re-tag", () => {
    setup()
    render(<DraftAction release={{ ...draft, tag: '3.2.0', kind: 'final', image_version: '3.2.0-rc1', git_ref: undefined, source_tag: '3.2.0-rc1' }} />)

    expect(screen.queryByLabelText('Référence Git à construire')).toBeNull()
    expect(screen.getByRole('button', { name: 'Lancer la release' })).toBeEnabled()
  })

  it('supprime le brouillon après confirmation', async () => {
    const calls = setup()
    const user = userEvent.setup()
    render(<DraftAction release={draft} />)

    await user.click(screen.getByRole('button', { name: 'Supprimer le brouillon' }))
    await user.click(screen.getByRole('button', { name: 'Supprimer' }))

    await waitFor(() => {
      expect(calls.some((call) => call.method === 'DELETE')).toBe(true)
    })
  })
})
