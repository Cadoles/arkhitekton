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
      type: 'release.created',
      label: 'Release déclarée',
      fields: ['release_id', 'tag', 'kind'],
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
    { kind: 'pipeline', ref: 'unit-tests', label: 'Tests unitaires', produces: ['results'] },
    {
      kind: 'pipeline',
      ref: 'build',
      label: "Construction d'image",
      produces: ['image'],
      fields: [
        { name: 'image-name', label: 'Dépôt publié', required: true, placeholder: 'portal' },
        { name: 'target', label: 'Cible du Dockerfile', required: true },
      ],
    },
    {
      kind: 'action',
      ref: 'gitea.status',
      label: 'Statut de commit',
      source: 'gitea',
      summary: 'Pose un statut sur un commit.',
      fields: [
        {
          name: 'state',
          label: 'État',
          required: true,
          options: ['pending', 'success', 'failure', 'error'],
        },
      ],
    },
  ],
  when: ['success', 'failure', 'always'],
  // Les rôles d'étape, et ce que chacun exige. L'éditeur ne code pas la table :
  // une liste écrite ici divergerait du binaire au premier ajout.
  roles: [
    {
      role: 'build',
      label: 'Construction',
      requires: 'image',
      advances: 'la release passe en vérification, ou échoue',
    },
    {
      role: 'test',
      label: 'Vérification',
      requires: 'results',
      advances: 'la release devient prête, ou échoue',
    },
  ],
}

// L'automatisation d'une release, telle que la reprise l'aurait composée.
const automatisation = {
  plan: {
    stages: [
      {
        role: 'build',
        gestes: [
          {
            kind: 'pipeline',
            ref: 'build',
            label: 'portal',
            params: { 'image-name': 'portal', target: 'web' },
          },
        ],
      },
      {
        role: 'test',
        gestes: [
          { kind: 'pipeline', ref: 'unit-tests', label: 'Tests unitaires', weight: 40 },
        ],
      },
    ],
  },
  declared: true,
  updated_by: 'system:reprise',
}

function armer(reactions: unknown[], automation: unknown = automatisation, schedules: unknown[] = []) {
  return mockFetch([
    { path: '/automations/catalogue', body: catalogue },
    { path: '/automation', body: automation },
    { path: '/reactions', body: { reactions, project: 'mse' } },
    { path: '/schedules', body: { schedules } },
    { path: '/schedules', method: 'POST', body: { id: 4 } },
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
    expect(await screen.findByRole('option', { name: 'Release déclarée' })).toBeInTheDocument()
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
    // Le geste d'un plugin est annoncé par sa source : « Statut de commit » ne
    // parle pas à la même forge selon le plugin qui le fournit.
    expect(screen.getByRole('option', { name: 'Statut de commit (gitea)' })).toBeInTheDocument()
  })

  it('suggère un jeu fermé sans interdire le gabarit qui le remplira', async () => {
    armer([])
    render(<AutomationsScreen />)

    await userEvent.click(await screen.findByRole('button', { name: /Déclarer une réaction/ }))

    const listes = await screen.findAllByRole('combobox')
    const ajout = listes.at(-1)
    if (!ajout) throw new Error("aucun sélecteur d'ajout de geste")
    await userEvent.selectOptions(ajout, 'action:gitea.status')

    // Une saisie libre adossée à des suggestions, et non un sélecteur fermé : le
    // serveur laisse un gabarit traverser un jeu fermé, parce qu'un état tiré de
    // la charge du fait ne peut pas s'écrire en dur. Un `select` interdirait
    // `{kind}` que le moteur accepte — quitte à le refuser après interpolation,
    // ce qui est le travail du serveur et non celui du formulaire.
    // Un `input` adossé à une `datalist` porte le rôle `combobox` : c'est
    // précisément la saisie libre suggérée qu'on veut ici.
    const champ = screen.getByRole('combobox', { name: /État/ })
    await userEvent.type(champ, '{{kind}')
    expect(champ).toHaveValue('{kind}')
    expect(champ).toHaveAttribute('list')
  })

  it("n'offre pas la condition d'échec sur la première étape", async () => {
    armer([])
    render(<AutomationsScreen />)

    await userEvent.click(await screen.findByRole('button', { name: /Déclarer une réaction/ }))

    // Le dernier sélecteur de l'éditeur est celui qui ajoute un geste : le
    // désigner par son rang est fragile, mais le désigner par son libellé le
    // serait davantage — l'option vide qui l'amorce n'a pas de nom stable.
    const listes = await screen.findAllByRole('combobox')
    const ajout = listes.at(-1)
    if (!ajout) throw new Error("aucun sélecteur d'ajout de geste")
    await userEvent.selectOptions(ajout, 'action:webhook')

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

/*
 * Le plan d'une release.
 *
 * Ce que le projet construit, puis vérifie — la déclaration qui remplace la
 * colonne `targets` et le drapeau `verification` du catalogue (doc 15 §15.5).
 */

describe("le plan d'une release", () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  it("nomme chaque étape par ce que sa conclusion fait à la release", async () => {
    armer([])
    render(<AutomationsScreen />)

    // Le rôle se lit par son effet, pas par son nom : « build » n'apprend rien.
    expect(await screen.findByText('Construction')).toBeInTheDocument()
    expect(
      screen.getByText('la release passe en vérification, ou échoue'),
    ).toBeInTheDocument()
    expect(screen.getByText('la release devient prête, ou échoue')).toBeInTheDocument()
  })

  it('distingue une automatisation vidée d’une automatisation jamais déclarée', async () => {
    // Jamais déclarée : le worker la composera depuis les cibles. Le dire évite
    // d'annoncer « ce projet ne construit plus » à qui attend simplement la
    // reprise.
    armer([], { plan: { stages: [] }, declared: false })
    render(<AutomationsScreen />)

    expect(await screen.findByText(/composée depuis les cibles du projet/)).toBeInTheDocument()
  })

  it('dit qu’un projet vidé ne construit plus', async () => {
    armer([], { plan: { stages: [] }, declared: true })
    render(<AutomationsScreen />)

    expect(await screen.findByText(/ne construit rien/)).toBeInTheDocument()
  })

  it("n'offre dans une étape de construction que ce qui produit une image", async () => {
    armer([])
    render(<AutomationsScreen />)

    await userEvent.click(await screen.findByRole('button', { name: 'Modifier' }))

    // L'étape reprise porte le rôle `build` : seuls les pipelines qui déclarent
    // `image` y entrent. Le serveur refuse de toute façon, mais un formulaire qui
    // laisse choisir puis refuse est un formulaire qu'on apprend à contourner.
    const ajouts = await screen.findAllByRole('combobox')
    const premierAjout = ajouts.find((champ) =>
      Array.from(champ.querySelectorAll('option')).some(
        (option) => option.textContent === 'ajouter un geste…',
      ),
    )
    if (!premierAjout) throw new Error("aucun sélecteur d'ajout de geste")

    const libelles = Array.from(premierAjout.querySelectorAll('option')).map(
      (option) => option.textContent,
    )
    expect(libelles.some((libelle) => libelle?.includes("Construction d'image"))).toBe(true)
    expect(libelles.some((libelle) => libelle?.includes('Tests unitaires'))).toBe(false)
    expect(libelles.some((libelle) => libelle?.includes('Appel sortant'))).toBe(false)
  })
})

/*
 * Le poids d'un geste au score (doc 15 §15.6).
 */

describe('le poids au score', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  it("ne se saisit que dans une étape de vérification", async () => {
    armer([])
    render(<AutomationsScreen />)

    await userEvent.click(await screen.findByRole('button', { name: 'Modifier' }))

    // Une seule saisie de poids : celle du geste de vérification. Le geste de
    // construction n'en a pas — un poids que rien ne lit est pire qu'un champ
    // absent, et le serveur le refuse.
    const poids = await screen.findAllByLabelText(/Poids au score/)
    expect(poids).toHaveLength(1)
    expect(poids[0]).toHaveValue(40)
  })

  it("distingue un poids vide d'un poids nul", async () => {
    armer([])
    render(<AutomationsScreen />)

    await userEvent.click(await screen.findByRole('button', { name: 'Modifier' }))
    const champ = await screen.findByLabelText(/Poids au score/)

    await userEvent.clear(champ)
    // Vide : le geste reprend la pondération d'origine. Ce n'est pas zéro.
    expect(await screen.findByText(/pondération d'origine/)).toBeInTheDocument()

    await userEvent.type(champ, '0')
    expect(await screen.findByText(/ne note pas la release/)).toBeInTheDocument()
  })
})

/*
 * Les planifications : les mêmes plans, ouverts par une horloge.
 */

describe('les planifications', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  it('dit quand une planification tire, et ce que son dernier tic a donné', async () => {
    armer([], automatisation, [
      {
        id: 4,
        name: 'Intégration',
        cron: '0 8,12,16 * * 1-5',
        timezone: 'Europe/Paris',
        ref: 'migration-symfony',
        enabled: true,
        plan: { stages: [{ gestes: [{ kind: 'pipeline', ref: 'unit-tests' }] }] },
        next_fire_at: '2026-11-02T11:00:00Z',
        last_fired_at: '2026-11-02T07:00:00Z',
        last_outcome: 'skipped: the previous run is still going',
      },
    ])
    render(<AutomationsScreen />)

    expect(await screen.findByText('Intégration')).toBeInTheDocument()
    expect(screen.getByText('0 8,12,16 * * 1-5')).toBeInTheDocument()
    expect(screen.getByText('migration-symfony')).toBeInTheDocument()
    expect(screen.getByText(/Prochain tic/)).toBeInTheDocument()
    expect(screen.getByText(/the previous run is still going/)).toBeInTheDocument()
  })

  it('envoie l’expression, le fuseau et la branche que l’on saisit', async () => {
    const calls = armer([])
    render(<AutomationsScreen />)

    await userEvent.click(await screen.findByRole('button', { name: 'Planifier' }))
    await userEvent.type(screen.getByLabelText('Nom'), 'Audit du lundi')
    await userEvent.type(screen.getByLabelText('Quand (cron)'), '0 5 * * MON')
    await userEvent.clear(screen.getByLabelText('Fuseau'))
    await userEvent.type(screen.getByLabelText('Fuseau'), 'Europe/Paris')
    await userEvent.type(screen.getByLabelText('Branche ou tag'), 'main')
    await userEvent.click(screen.getByRole('button', { name: 'Enregistrer' }))

    const sent = calls.find((call) => call.method === 'POST' && call.url.includes('/schedules'))
    expect(sent?.url).toContain('/projects/mse/schedules')
    expect(sent?.body).toMatchObject({
      name: 'Audit du lundi',
      cron: '0 5 * * MON',
      timezone: 'Europe/Paris',
      ref: 'main',
      enabled: true,
    })
  })
})
