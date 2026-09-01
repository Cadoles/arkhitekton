import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { CreateReleaseButton } from './CreateReleaseButton'
import { setProjects, selectProject } from '../../lib/project'
import { mockFetch, type Call } from '../../test/http'
import { render } from '../../test/render'
import type { Version, VersionRelease } from './api'

/*
 * Ce que le formulaire de création **envoie**.
 *
 * C'est là qu'un défaut s'est produit, et il n'était détectable qu'en cliquant :
 * le formulaire affichait la première nature cochée et soumettait son état
 * initial — la chaîne vide. Créer une release sans toucher au choix affiché
 * échouait sur « nature de release inconnue », en désignant une valeur que
 * l'utilisateur n'avait jamais vue.
 */

const version: Version = {
  id: 7,
  name: 'v3.2.0',
  status: 'open',
  due_date: null,
  synced_at: '2026-09-01T08:00:00Z',
  pending: false,
  completion: { counted: 2, done: 2, ratio: 1, weighted_ratio: 1 },
  open_anomalies: 0,
  releases: 0,
}

const cycle = [
  { id: 'pre', label: 'Pré-release', mode: 'build' as const },
  { id: 'final', label: 'Finale', mode: 'retag' as const, source: 'pre' },
]

function releasesRoute(kinds = cycle) {
  return { path: '/releases', method: 'GET', body: { releases: [], kinds } }
}

function sessionRoute(role: string) {
  return {
    path: '/me',
    body: {
      subject: '1', username: 'vcarroy', email: '', role,
      projects: [{ slug: 'app', label: 'App' }],
    },
  }
}

function setup(routes: Parameters<typeof mockFetch>[0]): Call[] {
  setProjects([{ slug: 'app', label: 'App' }])
  selectProject('app')
  return mockFetch(routes)
}

async function openAndSubmit(calls: Call[], suggestions: Record<string, string>) {
  const user = userEvent.setup()

  render(
    <CreateReleaseButton version={version} releases={[] as VersionRelease[]} suggestions={suggestions} />,
  )

  await user.click(await screen.findByRole('button', { name: /créer une release/i }))
  // La nature n'est **pas** cliquée : c'est tout le sujet. L'écran en montre une
  // cochée, et c'est elle qui doit partir.
  await user.click(await screen.findByRole('button', { name: /^créer$/i }))
  // La confirmation est une étape à part : créer une release met un re-tag en
  // file vers le registre, action sortante que l'interface fait confirmer.
  await user.click(await screen.findByRole('button', { name: /créer la release/i }))

  await waitFor(() => {
    expect(calls.some((call) => call.method === 'POST')).toBe(true)
  })
  return calls.find((call) => call.method === 'POST')
}

describe('CreateReleaseButton', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  it("soumet la nature affichée quand l'utilisateur n'en a coché aucune", async () => {
    const calls = setup([
      sessionRoute('releaser'),
      releasesRoute(),
      { path: '/forge/refs', body: { configured: false, branches: [], tags: [] } },
      // La forme réelle de la réponse : l'écran affiche un lien vers la release
      // créée, et un corps qui n'a pas cette forme le ferait planter.
      {
        path: '/releases', method: 'POST', status: 201,
        body: { release: { tag: '3.2.0-rc1', kind: 'pre', state: 'draft' }, warnings: [] },
      },
    ])

    const post = await openAndSubmit(calls, { pre: '3.2.0-rc1', final: '3.2.0' })

    expect(post?.body).toMatchObject({ kind: 'pre' })
  })

  it('emploie le tag proposé par le serveur, sans le recomposer', async () => {
    const calls = setup([
      sessionRoute('releaser'),
      releasesRoute(),
      { path: '/forge/refs', body: { configured: false, branches: [], tags: [] } },
      {
        path: '/releases', method: 'POST', status: 201,
        body: { release: { tag: 'build-4712', kind: 'pre', state: 'draft' }, warnings: [] },
      },
    ])

    // Une suggestion qui ne ressemble à rien de calculable : si l'écran la
    // reproduit, c'est qu'il ne l'a pas inventée.
    const post = await openAndSubmit(calls, { pre: 'build-4712', final: 'build-4712' })

    expect(post?.body).toMatchObject({ tag: 'build-4712' })
  })

  it('propose les natures que le projet déclare, et non une liste figée', async () => {
    const user = userEvent.setup()
    setup([
      sessionRoute('releaser'),
      releasesRoute([
        { id: 'alpha', label: 'Alpha', mode: 'build' as const },
        { id: 'ga', label: 'Livraison', mode: 'retag' as const, source: 'alpha' },
      ]),
      { path: '/forge/refs', body: { configured: false, branches: [], tags: [] } },
    ])

    render(<CreateReleaseButton version={version} releases={[]} suggestions={{ alpha: '3.2.0-a1' }} />)
    await user.click(await screen.findByRole('button', { name: /créer une release/i }))

    expect(await screen.findByText('Alpha')).toBeInTheDocument()
    expect(screen.getByText('Livraison')).toBeInTheDocument()
    // Le vocabulaire du MSE ne doit plus être écrit nulle part dans l'écran.
    expect(screen.queryByText('Pré-release')).not.toBeInTheDocument()
  })
})
