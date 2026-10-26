import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render as baseRender } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import type { ReactElement, ReactNode } from 'react'

/*
 * Le décor minimal d'un composant de cette interface.
 *
 * Trois fournisseurs, parce que presque tout composant en dépend : le client de
 * requêtes, le routeur, et rien d'autre. En ajouter davantage rendrait les tests
 * dépendants de contextes qu'ils n'éprouvent pas.
 */

/**
 * Construit un client de requêtes adapté au test.
 *
 * Sans réessai et sans cache entre deux tests : une requête qui échoue doit le
 * faire tout de suite, et un cache partagé ferait passer un test grâce aux
 * données du précédent — la panne la plus difficile à comprendre dans une suite.
 */
export function testQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0, staleTime: 0 },
      mutations: { retry: false },
    },
  })
}

export function render(ui: ReactElement, client: QueryClient = testQueryClient()) {
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <MemoryRouter>{children}</MemoryRouter>
    </QueryClientProvider>
  )
  return { client, ...baseRender(ui, { wrapper }) }
}
