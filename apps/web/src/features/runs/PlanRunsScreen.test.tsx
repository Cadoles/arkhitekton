import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import { render } from '../../test/render'
import { mockFetch } from '../../test/http'
import { selectProject, setProjects } from '../../lib/project'
import { PlanRunsScreen } from './PlanRunsScreen'

/*
 * Les exécutions d'automatisation.
 *
 * Ce que ces tests tiennent est la raison d'être de l'écran : un moteur qu'on ne
 * peut pas déboguer est pire qu'un comportement figé. Il doit donc montrer ce qui
 * a échoué, et **dire** ce qu'il ne montre pas.
 */

function armer(runs: unknown[], detailsVisible = true) {
  setProjects([{ slug: 'mse', label: 'MSE' }])
  selectProject('mse')
  return mockFetch([
    { path: '/plan-runs', body: { runs, project: 'mse', details_visible: detailsVisible } },
  ])
}

const executionEchouee = {
  id: 3,
  trigger: 'reaction:12',
  event_key: 'release.verified:42:900',
  state: 'failed',
  created_at: '2026-09-03T11:00:00Z',
  context: { tag: '1.8.0-rc1', outcome: 'failed' },
  stages: [
    {
      steps: [
        {
          stage: 0,
          position: 0,
          kind: 'action',
          ref: 'webhook',
          label: 'Prévenir',
          state: 'failed',
          detail: 'le destinataire a répondu 401: jeton expiré',
        },
      ],
    },
  ],
}

describe("les exécutions d'automatisation", () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  it("montre ce qui a échoué, et pourquoi", async () => {
    armer([executionEchouee])
    render(<PlanRunsScreen />)

    expect(await screen.findByText('Réaction n° 12')).toBeInTheDocument()
    expect(screen.getByText('échouée')).toBeInTheDocument()
    // Le motif, sans quoi « échoué » n'apprend rien et oblige à relancer pour voir.
    expect(screen.getByText(/401: jeton expiré/)).toBeInTheDocument()
    // Le sujet du fait déclencheur : sans lui, deux exécutions se ressemblent.
    expect(screen.getByText('1.8.0-rc1')).toBeInTheDocument()
  })

  it("nomme la chaîne intégrée plutôt que son identifiant technique", async () => {
    armer([
      {
        ...executionEchouee,
        id: 4,
        trigger: 'builtin:verification',
        state: 'succeeded',
        stages: [
          {
            label: 'Vérifications',
            steps: [
              {
                stage: 0,
                position: 0,
                kind: 'pipeline',
                ref: 'unit-tests',
                state: 'succeeded',
                job_id: 1381,
              },
            ],
          },
        ],
      },
    ])
    render(<PlanRunsScreen />)

    expect(await screen.findByText('Vérifications enchaînées')).toBeInTheDocument()
    // Le geste mène à son job : c'est là que vivent les journaux.
    expect(screen.getByText('job n° 1381')).toBeInTheDocument()
  })

  it("dit ce qu'il ne montre pas plutôt que de le taire", async () => {
    armer(
      [
        {
          ...executionEchouee,
          stages: [
            {
              steps: [
                {
                  stage: 0,
                  position: 0,
                  kind: 'action',
                  ref: 'webhook',
                  state: 'failed',
                  detail_withheld: true,
                },
              ],
            },
          ],
        },
      ],
      false,
    )
    render(<PlanRunsScreen />)

    // Sans cela, un lecteur non administrateur croirait qu'il n'y a rien à voir,
    // et n'appellerait pas quelqu'un qui peut regarder.
    //
    // Deux mentions, et les deux comptent : le bandeau dit la règle une fois,
    // la vignette dit qu'**ici** quelque chose est masqué.
    expect(await screen.findByText(/ce qu'un destinataire a répondu/)).toBeInTheDocument()
    expect(screen.getByText(/^détail réservé à l'administration$/)).toBeInTheDocument()
  })

  it("dit quand les exécutions apparaissent, plutôt que « aucune »", async () => {
    armer([])
    render(<PlanRunsScreen />)

    expect(await screen.findByText(/Aucune exécution/)).toBeInTheDocument()
    expect(screen.getByText(/quand une release se construit/)).toBeInTheDocument()
  })
})
