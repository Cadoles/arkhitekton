import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockFetch } from '../../test/http'
import { render } from '../../test/render'
import { SyncPanel } from './SyncPanel'

/*
 * Le panneau de synchronisation, devenu propre à un projet.
 *
 * Ce que ces tests éprouvent n'est pas l'affichage mais **l'adresse appelée** :
 * un panneau qui interrogerait l'ancienne route globale montrerait l'état d'un
 * projet sur l'écran d'un autre, et déclarerait un statut chez le mauvais client.
 * Rien à l'écran ne le dirait.
 */

afterEach(() => vi.unstubAllGlobals())

const etat = {
  configured: true,
  project: 'mse',
  sync: { last_run: null, last_ok: null, last_error: '' },
  unmapped: [{ status_name: 'Livré', issues: 3 }],
  ambiguous: [],
  pending: [],
  mapping: { Livré: 'doing' },
}

describe('SyncPanel', () => {
  it("interroge l'état du projet consulté, et non celui de l'instance", async () => {
    const calls = mockFetch([{ path: '/projects/mse/sync', body: etat }])

    render(<SyncPanel project="mse" />)
    await screen.findByText(/statut\(s\) sans projection déclarée/)

    expect(calls[0]!.url).toContain('/projects/mse/sync')
    expect(calls[0]!.url).not.toContain('/admin/sync')
  })

  // Déclarer un statut déplace un taux de complétion affiché au client : l'écrire
  // au mauvais projet le déplacerait chez quelqu'un d'autre.
  it('déclare la projection sur le projet consulté', async () => {
    const calls = mockFetch([
      { path: '/projects/mse/sync', body: etat },
      {
        path: '/projects/mse/sync/redmine/mapping/',
        method: 'PUT',
        body: { issues_moved: 3 },
      },
    ])

    render(<SyncPanel project="mse" />)
    await screen.findByText(/statut\(s\) sans projection déclarée/)
    await userEvent.click(screen.getAllByRole('button', { name: 'Déclarer' })[0]!)

    const ecriture = calls.find((call) => call.method === 'PUT')
    expect(ecriture?.url).toContain('/projects/mse/sync/redmine/mapping/')
  })

  // Un projet sans suivi n'est pas en panne de synchronisation : il n'en attend
  // aucune. Les confondre enverrait chercher une panne inexistante.
  it("dit « aucun suivi » plutôt que « jamais synchronisé »", async () => {
    mockFetch([{ path: '/projects/interne/sync', body: { ...etat, configured: false } }])

    render(<SyncPanel project="interne" />)

    expect(await screen.findByText(/ne déclare aucun outil de suivi/)).toBeInTheDocument()
  })
})
