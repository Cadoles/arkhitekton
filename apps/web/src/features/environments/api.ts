import { useQuery } from '@tanstack/react-query'
import { request } from '../../lib/api'

/**
 * D'où vient l'information affichée.
 *
 * `portaldata` atteste qu'une version **tourne** et que ses migrations ont
 * abouti ; `git` seulement que Flux a retenu ce tag — l'image peut n'avoir jamais
 * démarré ; `unobservable` qu'on ne peut rien en dire. Les trois ne se
 * remplacent pas : un même libellé pour les trois ferait passer une intention
 * pour un constat (B8).
 */
export type Resolution = 'portaldata' | 'git' | 'unobservable'

export type LastPromotion = {
  tag: string
  release: string
  pushed_at: string
  job_id?: number
}

export type Environment = {
  id: string
  label: string
  rank: number
  promotion_suffix: string
  observable: boolean
  /** Reçoit ce qui part chez l'utilisateur final : la confirmation exige alors
   *  de ressaisir le numéro de version. */
  sensitive: boolean
  portal_version?: string
  migration_state?: string
  flux_state?: string
  resolution?: Resolution
  observed_at?: string
  can_deploy: boolean
  last_promotion?: LastPromotion
}

export function useEnvironments() {
  return useQuery({
    queryKey: ['environments'],
    queryFn: () => request<{ environments: Environment[] }>('/environments'),
    // Un déploiement Flux prend des dizaines de secondes : rafraîchir plus vite
    // ne montrerait rien de plus, seulement du bruit.
    refetchInterval: 15000,
  })
}

/**
 * Ce qu'on peut dire d'un environnement, en une phrase.
 *
 * Le cas qui compte est le troisième : une promotion demandée que l'observation
 * ne confirme pas. Sans lui, « promu mais pas encore déployé » et « jamais
 * promu » s'affichent à l'identique — or le premier se règle en attendant, le
 * second en agissant (B7, B8).
 */
export function describe(environment: Environment): { tone: 'neutral' | 'warn' | 'ok'; text: string } {
  if (!environment.observable) {
    return {
      tone: 'neutral',
      text: "Cet environnement vit dans un cluster indépendant : son état n'est pas lisible depuis ici.",
    }
  }

  const promoted = environment.last_promotion
  const running = environment.portal_version

  if (!running) {
    return promoted
      ? { tone: 'warn', text: `${promoted.release} a été promue, mais aucune version n'est encore constatée.` }
      : { tone: 'neutral', text: "Aucune version constatée, et aucune promotion demandée." }
  }

  if (environment.resolution === 'git') {
    return {
      tone: 'warn',
      // Flux a retenu le tag ; rien ne dit que l'image a démarré ni que ses
      // migrations ont abouti.
      text: `Flux a retenu ${running}, mais le portail ne l'a pas encore confirmé.`,
    }
  }

  return { tone: 'ok', text: `${running} tourne, migrations abouties.` }
}

export type HistoryEntry = {
  release: string
  image_version: string
  tag: string
  pushed_at: string
  job_id?: number
  job_state?: string
  requested_by?: string
}

/**
 * Historique des promotions d'un environnement.
 *
 * La source est ce que le dashboard a **demandé**, et non les commits que Flux
 * écrit dans `mse-kustom`. Les deux diffèrent, et c'est l'écart qui intéresse :
 * une promotion qu'aucune ImagePolicy ne retient n'apparaît nulle part côté Git
 * alors qu'elle a bien eu lieu.
 */
export function useEnvironmentHistory(environment: string | undefined) {
  return useQuery({
    queryKey: ['environments', environment, 'history'],
    queryFn: () =>
      request<{ history: HistoryEntry[] }>(`/environments/${encodeURIComponent(environment as string)}/history`),
    enabled: Boolean(environment),
  })
}

/** Release rapprochée de la version observée sur un environnement.
 *
 *  Le rapprochement se fait sur `image_version`, jamais sur le tag : ce que le
 *  portail rapporte est la version bakée dans l'image, restée celle de la RC
 *  pour toute release finale produite par re-tag (B8, B17). */
export type DeployedRelease = {
  tag: string
  image_version: string
  kind: string
  state: string
  git_ref?: string
  created_by: string
  created_at: string
  /** Absent quand la version observée correspond à une release que le dashboard
   *  n'a jamais promue ici — un déploiement antérieur, ou par un autre chemin. */
  promoted_at?: string
  promoted_tag?: string
  /** Plusieurs releases portent cette image et aucune promotion ne tranche.
   *  Normal après un re-tag : la RC et la finale la partagent. */
  ambiguous: boolean
}

export type FlowStep = {
  id: string
  label: string
  rank: number
  current: boolean
  version: string
}

export type EnvironmentDetail = {
  id: string
  label: string
  rank: number
  promotion_suffix: string
  namespace: string
  observable: boolean
  sensitive: boolean
  can_deploy: boolean
  portal_version?: string
  migration_state?: string
  flux_state?: string
  resolution?: Resolution
  observed_at?: string
  flow: FlowStep[]
  /** Null quand la version observée ne correspond à aucune release connue :
   *  l'application est déployée depuis des années, le dashboard ne connaît que
   *  ce qu'il a lui-même produit. */
  deployed: DeployedRelease | null
  last_promotion?: LastPromotion
}

export function useEnvironment(id: string | undefined) {
  return useQuery({
    queryKey: ['environments', id],
    queryFn: () => request<EnvironmentDetail>(`/environments/${encodeURIComponent(id as string)}`),
    enabled: Boolean(id),
    refetchInterval: 15000,
  })
}
