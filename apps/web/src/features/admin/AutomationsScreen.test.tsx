import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { render } from '../../test/render'
import { mockFetch } from '../../test/http'
import { AutomationsScreen } from './AutomationsScreen'

/*
 * L'éditeur d'automatisations.
 *
 * Ce que ces tests tiennent n'est pas la mise en page mais **l'absence de liste
 * codée** : les faits écoutables, les gestes et les conditions viennent du
 * serveur. Une liste écrite ici divergerait du binaire au premier ajout, et
 * l'écart se verrait par un geste qu'on peut choisir et que rien n'exécute.
 */

const catalogue = {
  events: [
    {
      type: 'release.verified',
      label: 'Vérifications conclues',
      fields: ['release_id', 'tag', 'outcome'],
    },
    {
      type: 'gitea.pull_request.opened',
      label: 'Demande de fusion ouverte',
      source: 'gitea',
      fields: ['number', 'title'],
    },
  ],
  gestes: [
    {
      kind: 'action',
      ref: 'webhook',
      label: 'Appel sortant',
      summary: 'Émet un POST JSON vers une URL.',
      fields: [{ name: 'url', label: 'URL', required: true }],
    },
    { kind: 'pipeline', ref: 'unit-tests', label: 'Tests unitaires' },
  ],
  when: ['success', 'failure', 'always'],
}

function armer(reactions: unknown[]) {
  return mockFetch([
    { path: '/automations/catalogue', body: catalogue },
    { path: '/reactions', body: { reactions, project: 'mse' } },
  ])
}

// L'écran vit sous la coquille d'un projet, qui lui passe celui-ci en contexte.
vi.mock('react-router', async () => {
  const actual = await vi.importActual<Record<string, unknown>>('react-router')
  return { ...actual, useOutletContext: () => ({ slug: 'mse', label: 'MSE' }) }
})

describe("l'éditeur d'automatisations", () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  it('propose les faits que le serveur déclare, plugins compris', async () => {
    armer([])
    render(<AutomationsScreen />)

    await userEvent.click(await screen.findByRole('button', { name: /Déclarer une réaction/ }))

    // Les deux viennent du catalogue : celui du noyau et celui du plugin, ce
    // dernier annoncé par sa source.
    expect(await screen.findByRole('option', { name: 'Vérifications conclues' })).toBeInTheDocument()
    expect(
      screen.getByRole('option', { name: 'Demande de fusion ouverte (gitea)' }),
    ).toBeInTheDocument()
  })

  it('propose les gestes que le serveur déclare, actions et pipelines', async () => {
    armer([])
    render(<AutomationsScreen />)

    await userEvent.click(await screen.findByRole('button', { name: /Déclarer une réaction/ }))

    expect(await screen.findByRole('option', { name: 'Appel sortant (action)' })).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'Tests unitaires (pipeline)' })).toBeInTheDocument()
  })

  it("n'offre pas la condition d'échec sur la première étape", async () => {
    armer([])
    render(<AutomationsScreen />)

    await userEvent.click(await screen.findByRole('button', { name: /Déclarer une réaction/ }))

    // Le dernier sélecteur de l'éditeur est celui qui ajoute un geste : le
    // désigner par son rang est fragile, mais le désigner par son libellé le
    // serait davantage — l'option vide qui l'amorce n'a pas de nom stable.
    const listes = await screen.findAllByRole('combobox')
    await userEvent.selectOptions(listes[listes.length - 1], 'action:webhook')

    // Rien ne précède la première étape : un geste conditionné à l'échec n'y
    // partirait jamais, et le serveur le refuse. L'écran ne le propose donc pas.
    expect(screen.queryByRole('option', { name: /si elle a échoué/ })).not.toBeInTheDocument()
    expect(screen.getByRole('option', { name: /dans tous les cas/ })).toBeInTheDocument()
  })

  it('affiche une réaction inerte sans la cacher', async () => {
    armer([
      {
        id: 1,
        name: 'Prévenir sur PR',
        event_type: 'gitea.pull_request.opened',
        enabled: true,
        match: {},
        plan: { stages: [{ gestes: [{ kind: 'action', ref: 'webhook' }] }] },
        inert: true,
        inert_reason: "ce fait n'est plus produit : le plugin qui l'observe est éteint",
      },
    ])
    render(<AutomationsScreen />)

    // Inerte plutôt qu'effacée : une extinction peut être temporaire, et effacer
    // ferait perdre une configuration.
    expect(await screen.findByText('Prévenir sur PR')).toBeInTheDocument()
    expect(screen.getByText('inerte')).toBeInTheDocument()
    expect(screen.getByText(/le plugin qui l'observe est éteint/)).toBeInTheDocument()
  })

  it("dit à quoi sert une réaction quand il n'y en a aucune", async () => {
    armer([])
    render(<AutomationsScreen />)

    // « Aucune réaction » seul n'apprend rien : le message dit ce qu'une réaction
    // fait, pour que la page vide soit un point de départ.
    expect(await screen.findByText(/Aucune réaction déclarée/)).toBeInTheDocument()
    expect(screen.getByText(/prévenir une messagerie/)).toBeInTheDocument()
  })
})
