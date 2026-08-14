import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request } from '../../lib/api'

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


export type ManualActions = {
  markdown: string
  updated_by?: string
  updated_at?: string
}

export type ReleaseJob = {
  id: number
  kind: string
  state: string
  environment?: string
  run_name?: string
  requested_by: string
  requested_at: string
  finished_at?: string
}

export type Image = {
  repository: string
  tag: string
  /** Vide quand le tag n'existe pas encore dans le registre — une promotion mise
   *  en file n'a pas encore poussé le sien. */
  digest?: string
}

/** Détail d'une release : la liste n'en porte que l'en-tête. */
export type ReleaseDetail = Release & {
  jobs?: ReleaseJob[]
  images?: Image[]
  /** Dit pourquoi les digests manquent. « Pas de digest » et « impossible de le
   *  lire » n'appellent pas la même réaction. */
  images_error?: string
  manual_actions?: ManualActions
}

export function useRelease(tag: string | undefined) {
  return useQuery({
    queryKey: ['releases', tag],
    queryFn: () => request<ReleaseDetail>(`/releases/${encodeURIComponent(tag as string)}`),
    enabled: Boolean(tag),
  })
}

/**
 * Enregistrement des actions manuelles.
 *
 * L'auteur et la date ne sont pas envoyés : le serveur les pose. Ce texte finit
 * dans un document contractuel, et sa provenance ne doit pas dépendre de ce qu'un
 * client veut bien déclarer.
 */
export function useSaveManualActions(tag: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (markdown: string) =>
      request<ManualActions>(`/releases/${encodeURIComponent(tag)}/actions`, {
        method: 'PUT',
        body: JSON.stringify({ markdown }),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['releases', tag] })
    },
  })
}
