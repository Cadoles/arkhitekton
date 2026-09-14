import { describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import { render } from '../../test/render'
import { SignalGrid } from './SignalGrid'
import type { SignalScore } from './tests'

/*
 * Les cases du score, une par geste déclaré.
 *
 * La grille rendait six tuiles fixes, une par signal. Un projet qui ne déclare
 * ni campagne E2E ni tests de charge en voyait deux affichant « non mesuré » —
 * une absence qui se lit comme un manque, alors que personne ne les avait
 * demandées (doc 15 §15.7).
 */

// L'écran vit sous un projet sélectionné, que les requêtes de détail emploient.
vi.mock('../../lib/project', async () => {
  const actual = await vi.importActual<Record<string, unknown>>('../../lib/project')
  return { ...actual, projectPath: (path: string) => `/projects/mse${path}` }
})

function composant(overrides: Partial<SignalScore> & Pick<SignalScore, 'key'>): SignalScore {
  return {
    label: 'Composant',
    signal: 'unit',
    value: 100,
    weight: 100,
    declared: 25,
    measured: true,
    health: 'healthy',
    ...overrides,
  }
}

describe('les cases du score', () => {
  it('rend une case par composant, et pas six cases écrites d’avance', () => {
    render(
      <SignalGrid
        tag="1.8.0"
        jobs={[]}
        signals={[
          composant({ key: 'geste:1.0', label: 'Tests unitaires', ref: 'unit-tests', family: 'unit' }),
          composant({ key: 'geste:1.1', label: 'Analyse statique', signal: 'code', family: 'static' }),
        ]}
      />,
    )

    expect(screen.getByText('Tests unitaires')).toBeInTheDocument()
    expect(screen.getByText('Analyse statique')).toBeInTheDocument()
    // Rien d'autre : les signaux qu'aucun geste ne déclare n'ont pas de case.
    expect(screen.queryByText('Tests E2E')).not.toBeInTheDocument()
    expect(screen.queryByText('Performance')).not.toBeInTheDocument()
  })

  it('distingue deux campagnes de même nature', () => {
    render(
      <SignalGrid
        tag="1.8.0"
        jobs={[]}
        signals={[
          composant({
            key: 'geste:1.0', label: 'E2E portail',
            signal: 'e2e', family: 'e2e', ref: 'e2e-tests', value: 100,
          }),
          composant({
            key: 'geste:1.1', label: 'E2E back-office',
            signal: 'e2e', family: 'e2e', ref: 'e2e-tests', value: 40, health: 'critical',
          }),
        ]}
      />,
    )

    // Deux cases, deux libellés : le signal qu'elles partagent ne les confond
    // plus, et c'est tout l'objet du changement d'axe.
    expect(screen.getByText('E2E portail')).toBeInTheDocument()
    expect(screen.getByText('E2E back-office')).toBeInTheDocument()
    expect(screen.getByText('100')).toBeInTheDocument()
    expect(screen.getByText('40')).toBeInTheDocument()
  })

  it('n’ouvre pas de détail pour un composant qui n’en publie pas', () => {
    render(
      <SignalGrid
        tag="1.8.0"
        jobs={[]}
        signals={[composant({ key: 'geste:1.0', label: 'Déploiement de recette', family: undefined })]}
      />,
    )

    // Une tuile inerte : un bouton qui n'ouvre rien se clique une fois et déçoit.
    expect(
      screen.queryByRole('button', { name: /Ouvrir le détail/ }),
    ).not.toBeInTheDocument()
  })

  it('n’affiche pas de valeur pour un composant non mesuré', () => {
    render(
      <SignalGrid
        tag="1.8.0"
        jobs={[]}
        signals={[
          composant({
            key: 'geste:1.0', label: 'Audit de sécurité', signal: 'security',
            family: 'security', measured: false, value: 0, weight: 0,
            health: 'na', detail: 'non exécuté',
          }),
        ]}
      />,
    )

    // Un composant non mesuré n'est pas un composant à zéro : les deux
    // afficheraient le même chiffre, et les confondre ferait lire comme un échec
    // ce qui n'a simplement pas tourné (B11).
    expect(screen.getByText('—')).toBeInTheDocument()
    expect(screen.getByText('non exécuté')).toBeInTheDocument()
  })
})
