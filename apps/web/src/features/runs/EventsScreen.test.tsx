import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import { render } from '../../test/render'
import { mockFetch } from '../../test/http'
import { selectProject, setProjects } from '../../lib/project'
import { EventsScreen } from './EventsScreen'

/*
 * Le fil des faits.
 *
 * Ce que ces tests tiennent n'est pas l'affichage mais **l'ouverture** : un type
 * que l'interface ne connaît pas doit rester lisible. Les faits d'un plugin
 * arriveront avec leurs propres libellés, et une table fermée les rendrait muets
 * — un fil qui cache des lignes est pire qu'un fil vide, parce qu'il fait croire
 * qu'il ne s'est rien passé.
 */

function armer(events: unknown[]) {
  setProjects([{ slug: 'mse', label: 'MSE' }])
  selectProject('mse')
  return mockFetch([{ path: '/events', body: { events, project: 'mse' } }])
}

describe("le fil des faits d'un projet", () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  it('affiche un fait du noyau avec son libellé et son issue', async () => {
    armer([
      {
        id: 1,
        type: 'release.built',
        subject: '1.8.0-rc1',
        payload: { outcome: 'succeeded', tag: '1.8.0-rc1' },
        occurred_at: '2026-09-02T14:00:00Z',
        depth: 0,
      },
    ])

    render(<EventsScreen />)

    expect(await screen.findByText('Construction conclue')).toBeInTheDocument()
    expect(screen.getByText('1.8.0-rc1')).toBeInTheDocument()
    expect(screen.getByText('réussi')).toBeInTheDocument()
  })

  it("affiche un type inconnu tel quel plutôt que de l'escamoter", async () => {
    armer([
      {
        id: 2,
        type: 'gitea.pull_request.opened',
        source: 'gitea',
        subject: '#412',
        payload: {},
        occurred_at: '2026-09-02T15:00:00Z',
        depth: 0,
      },
    ])

    render(<EventsScreen />)

    // Le type brut fait office de libellé : c'est moins joli qu'une traduction,
    // et infiniment mieux qu'une ligne absente.
    expect(await screen.findByText('gitea.pull_request.opened')).toBeInTheDocument()
    // La source n'apparaît que pour un plugin.
    expect(screen.getByText('gitea')).toBeInTheDocument()
  })

  it("distingue un fil vide d'une absence de réaction", async () => {
    armer([])

    render(<EventsScreen />)

    // Le message dit **pourquoi** c'est vide et quand cela se remplit. « Aucun
    // fait » seul laisserait croire à une panne.
    expect(await screen.findByText(/Aucun fait constaté/)).toBeInTheDocument()
    expect(screen.getByText(/au fil des créations de release/)).toBeInTheDocument()
  })

  it("ne montre l'instant de constat que lorsqu'il diffère du fait", async () => {
    armer([
      {
        id: 3,
        type: 'release.created',
        subject: '1.9.0-rc1',
        payload: {},
        occurred_at: '2026-09-02T14:00:00Z',
        depth: 0,
      },
    ])

    render(<EventsScreen />)

    await screen.findByText('Release déclarée')
    // Le cas courant : les deux instants coïncident, et le serveur omet le
    // second. Deux dates identiques sur chaque ligne finiraient par n'être plus
    // lues du tout.
    expect(screen.queryByText(/constaté le/)).not.toBeInTheDocument()
  })
})
