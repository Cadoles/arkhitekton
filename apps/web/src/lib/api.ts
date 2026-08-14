/*
 * Client HTTP du dashboard.
 *
 * Le backend applique le motif BFF : la session vit côté serveur, le navigateur
 * ne détient qu'un cookie opaque et ne voit jamais de jeton (doc 07 §7.1). Il n'y
 * a donc aucun en-tête d'autorisation à poser ici — seulement un cookie à laisser
 * partir, et un jeton CSRF à renvoyer sur les méthodes non sûres.
 */

const BASE = '/api/v1'

/** Nom du cookie de double soumission, posé non-HttpOnly à dessein : c'est ce
 * qui permet à ce module de le relire pour le renvoyer en en-tête. */
const CSRF_COOKIE = 'mse_csrf'
const CSRF_HEADER = 'X-CSRF-Token'

/** Erreur portant le statut HTTP, pour que l'appelant distingue les cas. */
export class ApiError extends Error {
  // Champs déclarés puis affectés, et non paramètres de constructeur : ces
  // derniers sont interdits par `erasableSyntaxOnly`, qui garantit que le
  // TypeScript du projet reste effaçable — donc compilable par n'importe quel
  // outil qui se contente d'ôter les types.
  readonly status: number
  readonly detail: string | undefined

  constructor(status: number, message: string, detail?: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.detail = detail
  }

  /** Pas de session : la réponse attendue est une redirection vers la connexion. */
  get isUnauthenticated() {
    return this.status === 401
  }

  /**
   * Authentifié mais sans rôle, ou rôle insuffisant.
   *
   * Distinct de 401, et la distinction est fonctionnelle : un utilisateur connu
   * de Gitea mais pas encore habilité doit lire « votre compte n'a pas encore de
   * rôle », pas être renvoyé en boucle vers une connexion qui réussira toujours
   * (doc 07 §7.2).
   */
  get isForbidden() {
    return this.status === 403
  }
}

function csrfToken(): string | null {
  const match = document.cookie.match(new RegExp(`(?:^|; )${CSRF_COOKIE}=([^;]*)`))
  return match?.[1] ? decodeURIComponent(match[1]) : null
}

/** Corps d'erreur RFC 9457, tel que le produit le middleware Go. */
type Problem = { title?: string; detail?: string; status?: number }

export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const method = (init.method ?? 'GET').toUpperCase()
  const headers = new Headers(init.headers)

  if (method !== 'GET' && method !== 'HEAD') {
    const token = csrfToken()
    if (token) headers.set(CSRF_HEADER, token)
    if (init.body && !headers.has('Content-Type')) {
      headers.set('Content-Type', 'application/json')
    }
  }

  const response = await fetch(`${BASE}${path}`, {
    ...init,
    headers,
    // Indispensable : sans cela le cookie de session ne part pas, et toute
    // requête reviendrait en 401 sans que rien n'indique pourquoi.
    credentials: 'same-origin',
  })

  if (!response.ok) {
    let problem: Problem = {}
    try {
      problem = (await response.json()) as Problem
    } catch {
      // Une réponse d'erreur sans corps JSON reste une erreur : le statut suffit.
    }
    throw new ApiError(response.status, problem.title ?? `Erreur HTTP ${response.status}`, problem.detail)
  }

  if (response.status === 204) return undefined as T
  return (await response.json()) as T
}

/**
 * Construit l'URL d'un flux SSE.
 *
 * `EventSource` n'emprunte pas `request` : il faut donc lui donner un chemin
 * complet. L'oubli du préfixe est silencieux et coûteux — le serveur renvoie la
 * SPA avec un code 200, et le flux reste vide sans qu'aucune erreur n'apparaisse.
 */
export function streamURL(path: string): string {
  return `${BASE}${path}`
}

/** Redirige vers le fournisseur d'identité, en demandant le retour ici même. */
export function login(returnTo: string = window.location.pathname + window.location.search): void {
  // Navigation complète et non fetch : le flux OIDC est une suite de redirections
  // que le navigateur doit suivre lui-même.
  window.location.assign(`${BASE}/auth/login?redirect=${encodeURIComponent(returnTo)}`)
}

export async function logout(): Promise<void> {
  await request<void>('/auth/logout', { method: 'POST' })
  window.location.assign('/')
}
