/*
 * Ce que chaque test d'interface reçoit sans le demander.
 */

import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach, expect } from 'vitest'

// Le DOM est démonté entre deux tests. Sans cela, un `getByRole` du second
// trouverait l'arbre du premier — et le test passerait pour la mauvaise raison,
// ce qui est pire qu'un échec.
afterEach(cleanup)

/*
 * `matchMedia` n'existe pas dans jsdom.
 *
 * Le socle de l'interface l'interroge pour le thème sombre. Sans ce bouchon, le
 * moindre rendu échoue sur une erreur qui ne parle pas du composant testé.
 */
Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  }),
})

expect.extend({})
