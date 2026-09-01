import { vi } from 'vitest'

/*
 * Le serveur, réduit à ce qu'un test doit en dire.
 *
 * `fetch` est remplacé plutôt que le client de requêtes : c'est la frontière
 * réelle du navigateur, et la bouchonner plus haut laisserait le code du client
 * — les chemins, les entêtes, le corps envoyé — hors de portée des tests. Or
 * c'est précisément là qu'un défaut s'est produit : un formulaire qui envoyait
 * la mauvaise valeur.
 */

export type Route = {
  /** Fragment que l'URL doit contenir. */
  path: string
  method?: string
  status?: number
  body?: unknown
}

export type Call = { url: string; method: string; body: unknown }

/**
 * Installe un faux `fetch` et rend la liste des appels observés.
 *
 * Les appels sont conservés pour être **vérifiés** : ce qu'un écran envoie fait
 * partie de son comportement, au même titre que ce qu'il affiche.
 */
export function mockFetch(routes: Route[]): Call[] {
  const calls: Call[] = []

  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString()
    const method = init?.method ?? 'GET'
    const body = init?.body ? JSON.parse(init.body as string) : undefined
    calls.push({ url, method, body })

    const route = routes.find(
      (candidate) =>
        url.includes(candidate.path) && (candidate.method ?? 'GET') === method,
    )
    if (!route) {
      // Un appel non prévu est une erreur du test, pas du composant : le dire
      // vaut mieux qu'un `undefined` qui se manifestera trois assertions plus
      // loin.
      return Promise.reject(new Error(`aucune route de test pour ${method} ${url}`))
    }

    const status = route.status ?? 200
    return Promise.resolve(
      new Response(JSON.stringify(route.body ?? {}), {
        status,
        headers: { 'content-type': 'application/json' },
      }),
    )
  })

  return calls
}
