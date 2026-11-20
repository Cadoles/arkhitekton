import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Route, Routes, useParams } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mockFetch } from '../../test/http'
import { render } from '../../test/render'
import type { Project } from './api'
import { ProjectsScreen } from './ProjectsScreen'

/*
 * La liste des dépôts : ce qui n'est pas en service se voit et mène à sa page, et un dépôt
 * déclaré s'y met en service sans détour.
 */

const ready = {
  id: 1, slug: 'portal', repository: 'team/portal', label: 'Portal', uid: 'u1', targets: [],
  checked_at: '2026-11-20T10:00:00Z', check_ok: true, deploy_key: 'ssh-ed25519 A', deploy_key_active: true,
} as unknown as Project
const waiting = { ...ready, id: 2, slug: 'theme', repository: 'team/theme', label: 'Theme', deploy_key_active: false }

function ProjectPage() {
  const { slug } = useParams()
  return <p>page du projet {slug}</p>
}

function screenWithRoutes() {
  return (
    <Routes>
      <Route path="/" element={<ProjectsScreen />} />
      <Route path="/admin/projects/:slug" element={<ProjectPage />} />
    </Routes>
  )
}

describe('la liste des dépôts', () => {
  beforeEach(() => vi.unstubAllGlobals())

  it("montre ce qui n'est pas en service, sans bouton de test", async () => {
    mockFetch([{ path: '/projects', body: { projects: [ready, waiting], forge_url: '', image_registry: '', probe_configured: true } }])
    render(screenWithRoutes())

    const link = await screen.findByRole('link', { name: 'à mettre en service' })
    expect(link).toHaveAttribute('href', '/admin/projects/theme')
    expect(screen.getAllByRole('link', { name: 'à mettre en service' })).toHaveLength(1)
    expect(screen.queryByRole('button', { name: /Tester l'accès/ })).not.toBeInTheDocument()
  })

  it('mène au projet déclaré, pour le mettre en service', async () => {
    mockFetch([
      { path: '/projects', method: 'POST', status: 201, body: { id: 3, slug: 'new-app' } },
      { path: '/projects', body: { projects: [], forge_url: 'https://forge', image_registry: '', probe_configured: true } },
    ])
    render(screenWithRoutes())

    await userEvent.click(await screen.findByRole('button', { name: 'Déclarer un dépôt' }))
    await userEvent.type(screen.getByRole('textbox', { name: 'Dépôt' }), 'team/new-app')
    await userEvent.click(screen.getByRole('button', { name: 'Déclarer' }))

    expect(await screen.findByText('page du projet new-app')).toBeInTheDocument()
  })
})
