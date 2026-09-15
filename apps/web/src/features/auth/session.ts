import { useQuery } from '@tanstack/react-query'
import { ApiError, request } from '../../lib/api'
import { setProjects, type ProjectChoice } from '../../lib/project'

/** Identité renvoyée par `GET /api/v1/me`. */
export type Identity = {
  subject: string
  username: string
  email: string
  role: Role
  /**
   * Projets lisibles, tels que l'API les nomme dans ses chemins. Vide quand
   * aucun n'est déclaré : l'interface ne peut alors composer aucune URL de
   * release, et il vaut mieux le voir échouer que d'interroger
   * `/projects//releases`.
   */
  projects?: ProjectChoice[]
  /**
   * Modules allumés sur l'instance — `mse` pour les environnements et la
   * promotion. Absent ou vide : l'interface ne propose aucun de leurs écrans, et
   * leurs routes répondraient 404 (doc 16).
   */
  modules?: string[]
}

export type Role = 'viewer' | 'operator' | 'releaser' | 'admin'

/** Trois issues, et non deux : sans rôle n'est pas non authentifié. */
export type SessionState =
  | { kind: 'loading' }
  | { kind: 'anonymous' }
  | { kind: 'unauthorized' }
  | { kind: 'authenticated'; identity: Identity }
  | { kind: 'error'; message: string }

export function useSession(): SessionState {
  const query = useQuery({
    queryKey: ['me'],
    queryFn: async () => {
      const identity = await request<Identity>('/me')
      // Retenus ici parce que c'est la seule réponse qui les porte, et qu'elle
      // précède tout écran : les URL des releases et des versions en dépendent.
      setProjects(identity.projects ?? [])
      return identity
    },
    // Inutile de réessayer : 401 et 403 sont des réponses, pas des pannes.
    retry: (failureCount, error) =>
      !(error instanceof ApiError && (error.isUnauthenticated || error.isForbidden)) && failureCount < 2,
    staleTime: 5 * 60 * 1000,
  })

  if (query.isPending) return { kind: 'loading' }

  if (query.error) {
    if (query.error instanceof ApiError) {
      if (query.error.isUnauthenticated) return { kind: 'anonymous' }
      // 403 sur `/me` signifie « authentifié, aucun rôle » : le backend le
      // distingue explicitement de 401 pour que l'interface puisse expliquer la
      // situation au lieu de renvoyer vers une connexion qui réussira toujours.
      if (query.error.isForbidden) return { kind: 'unauthorized' }
      return { kind: 'error', message: query.error.message }
    }
    return { kind: 'error', message: 'Le serveur est injoignable.' }
  }

  return { kind: 'authenticated', identity: query.data }
}

/**
 * Dit si un module est allumé.
 *
 * Faux tant que la session n'est pas chargée : afficher l'onglet puis le retirer
 * ferait sauter la navigation, et proposer un écran dont les requêtes échoueront
 * est pire que le montrer une seconde plus tard.
 */
export function useModule(module: string): boolean {
  const session = useSession()
  return session.kind === 'authenticated' && (session.identity.modules ?? []).includes(module)
}

const ROLE_LABELS: Record<Role, string> = {
  viewer: 'Lecture',
  operator: 'Opérateur',
  releaser: 'Responsable de release',
  admin: 'Administrateur',
}

export function roleLabel(role: Role): string {
  return ROLE_LABELS[role] ?? role
}

const RANK: Record<Role, number> = { viewer: 0, operator: 1, releaser: 2, admin: 3 }

/**
 * Confort d'affichage uniquement.
 *
 * Masquer une action interdite évite un clic qui échouera ; cela ne protège
 * rien. Toute décision d'autorisation est prise côté serveur, sans exception
 * (doc 07 §7.2).
 */
export function hasAtLeast(role: Role, required: Role): boolean {
  return RANK[role] >= RANK[required]
}
