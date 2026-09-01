import { screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { VersionsScreen } from './VersionsScreen'
import { selectProject, setProjects } from '../../lib/project'
import { mockFetch } from '../../test/http'
import { render } from '../../test/render'

/*
 * Ce que l'écran des versions montre selon l'origine des versions.
 *
 * Deux défauts s'y sont produits, tous deux invisibles autrement qu'en
 * regardant : le bouton de déclaration manuelle apparaissait sur un projet
 * suivi — où l'outil fait autorité et où le serveur refuse —, et un projet sans
 * suivi lisait « Aucune version déclarée dans le projet Redmine », ce qui envoie
 * chercher une panne inexistante.
 */

function versionsRoute(tracked: boolean, everRan: boolean, versions: unknown[]) {
  return {
    path: '/versions',
    body: {
      versions,
      sync: { last_run: null, last_ok: null, ever_ran: everRan, tracked },
    },
  }
}

function setup(tracked: boolean, { everRan = true, versions = [] as unknown[] } = {}) {
  setProjects([{ slug: 'app', label: 'App' }])
  selectProject('app')
  return mockFetch([versionsRoute(tracked, everRan, versions)])
}

describe('VersionsScreen', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  it('ne propose pas de déclarer une version sur un projet suivi', async () => {
    setup(true)
    render(<VersionsScreen />)

    // L'attente porte sur un élément qui existe : chercher directement l'absence
    // passerait avant même le premier rendu, et le test serait vert sans avoir
    // rien vérifié.
    expect(await screen.findByText(/aucune version déclarée dans le suivi/i)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /déclarer une version/i })).not.toBeInTheDocument()
  })

  it('propose de déclarer une version sur un projet sans suivi', async () => {
    setup(false)
    render(<VersionsScreen />)

    expect(await screen.findByRole('button', { name: /déclarer une version/i })).toBeInTheDocument()
  })

  it("ne parle d'aucun outil de suivi à un projet qui n'en a pas", async () => {
    setup(false)
    render(<VersionsScreen />)

    expect(
      await screen.findByText(/ne déclare aucun outil de suivi/i),
    ).toBeInTheDocument()
    // Le mot qui envoyait chercher une panne : ce projet n'a jamais eu de
    // Redmine, et rien n'est à attendre d'une synchronisation.
    expect(screen.queryByText(/redmine/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/synchronisation.*jamais tourné/i)).not.toBeInTheDocument()
  })
})
