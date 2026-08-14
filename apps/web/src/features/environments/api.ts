import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
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

export type ReleaseState = 'draft' | 'building' | 'ready' | 'failed'
export type ReleaseKind = 'pre' | 'final' | 'patch'

/**
 * Cible de promotion, telle que le serveur la juge.
 *
 * `allowed` et `reason` viennent du serveur et ne sont pas recalculés ici : le
 * refus dépend de la plage SemVer de l'ImagePolicy et du rôle de l'appelant, deux
 * choses que le navigateur ne connaît pas. Le dupliquer donnerait une interface
 * qui autorise ce que l'API refuse — ou l'inverse.
 */
export type PromotionTarget = {
  environment: string
  label: string
  tag?: string
  allowed: boolean
  reason?: string
}

export type Promotion = {
  environment: string
  image: string
  tag: string
  digest?: string
  job_id?: number
  pushed_at: string
}

export type Release = {
  tag: string
  kind: ReleaseKind
  state: ReleaseState
  /** Version portée par l'image, distincte du tag sur toute finale re-taguée. */
  image_version: string
  git_ref?: string
  source_tag?: string
  created_by: string
  created_at: string
  released_at?: string
  promotions?: Promotion[]
  targets?: PromotionTarget[]
}

export function useReleases() {
  return useQuery({
    queryKey: ['releases'],
    queryFn: () => request<{ releases: Release[] }>('/releases'),
  })
}

export function useRelease(tag: string | undefined) {
  return useQuery({
    queryKey: ['releases', tag],
    queryFn: () => request<Release>(`/releases/${encodeURIComponent(tag as string)}`),
    enabled: Boolean(tag),
  })
}

export function usePromote() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({ tag, environment }: { tag: string; environment: string }) =>
      request<{ job_id: number; environment: string; tag: string }>(
        `/releases/${encodeURIComponent(tag)}/promote/${encodeURIComponent(environment)}`,
        { method: 'POST' },
      ),
    onSuccess: () => {
      // Trois vues changent d'un coup : la file reçoit le job, la release gagne
      // une promotion, l'environnement gagne une promotion demandée.
      void queryClient.invalidateQueries({ queryKey: ['queue'] })
      void queryClient.invalidateQueries({ queryKey: ['releases'] })
      void queryClient.invalidateQueries({ queryKey: ['environments'] })
    },
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
