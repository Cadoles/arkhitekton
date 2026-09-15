import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request } from '../../lib/api'
import { projectPath } from '../../lib/project'

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
  /** Les réglages de l'outil de déploiement, dans **son** vocabulaire. Rendus
   *  tels quels par l'API : le dashboard les transporte sans les lire, et un
   *  projet sans déployeur n'en a aucun. */
  deployer_settings?: Record<string, string>
  observable: boolean
  /** Reçoit ce qui part chez l'utilisateur final : la confirmation exige alors
   *  de ressaisir le numéro de version. */
  sensitive: boolean
  portal_version?: string
  migration_state?: string
  declared_state?: string
  resolution?: Resolution
  observed_at?: string
  can_deploy: boolean
  last_promotion?: LastPromotion
}

export function useEnvironments() {
  return useQuery({
    queryKey: ['environments'],
    queryFn: () => request<{ environments: Environment[] }>(projectPath('/environments')),
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
      request<{ history: HistoryEntry[] }>(projectPath(`/environments/${encodeURIComponent(environment as string)}/history`)),
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
  deployer_settings?: Record<string, string>
  namespace: string
  observable: boolean
  sensitive: boolean
  can_deploy: boolean
  portal_version?: string
  migration_state?: string
  declared_state?: string
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
    queryFn: () => request<EnvironmentDetail>(projectPath(`/environments/${encodeURIComponent(id as string)}`)),
    enabled: Boolean(id),
    refetchInterval: 15000,
  })
}

/*
 * Le suffixe de promotion, quand le déployeur en déclare un.
 *
 * Affiché parce qu'il ne se devine pas : Flux connaît `qualif` sous le nom
 * `testing`, et c'est ce nom-là qui part dans le tag. Le cacher rendrait toute
 * vérification impossible, et une confirmation qui ne le dirait pas demanderait
 * d'approuver à l'aveugle.
 *
 * Rendu `undefined` plutôt que chaîne vide quand il n'y en a pas : un
 * environnement d'un projet sans déployeur n'affiche alors **rien**, au lieu
 * d'une pastille vide qui ressemble à une donnée manquante.
 */
export function promotionSuffix(
  environment: { deployer_settings?: Record<string, string> },
): string | undefined {
  return environment.deployer_settings?.promotionSuffix || undefined
}

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

export function usePromote() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({ tag, environment }: { tag: string; environment: string }) =>
      request<{ job_id: number; environment: string; tag: string }>(
        projectPath(`/releases/${encodeURIComponent(tag)}/promote/${encodeURIComponent(environment)}`),
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

/** Les cibles de promotion d'une release, servies par le plugin MSE. */
export function useReleaseTargets(tag: string) {
  return useQuery({
    queryKey: ['releases', tag, 'targets'],
    queryFn: () =>
      request<{ targets: PromotionTarget[] }>(projectPath(`/releases/${encodeURIComponent(tag)}/targets`)),
  })
}

/** `pass` validé, `warn` validé sous réserve, `fail` refusé. L'attente est
 *  l'absence d'entrée, non une valeur : une ligne « en attente » supposerait de
 *  savoir à l'avance qui doit se prononcer. */
export type SignoffStatus = 'pass' | 'warn' | 'fail'

export type Signoff = {
  role: string
  status: SignoffStatus
  detail?: string
  decided_by: string
  decided_at: string
}

export type EnvironmentSignoffs = {
  environment: string
  label: string
  signoffs: Signoff[]
}

export function useSignoffs(tag: string) {
  return useQuery({
    queryKey: ['releases', tag, 'signoffs'],
    queryFn: () =>
      request<{ environments: EnvironmentSignoffs[] }>(
        projectPath(`/releases/${encodeURIComponent(tag)}/signoffs`),
      ),
    enabled: Boolean(tag),
  })
}

export function useSetSignoff(tag: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({
      environment,
      ...payload
    }: {
      environment: string
      role: string
      status: SignoffStatus
      detail: string
    }) =>
      request<void>(
        projectPath(`/releases/${encodeURIComponent(tag)}/signoffs/${encodeURIComponent(environment)}`),
        { method: 'PUT', body: JSON.stringify(payload) },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['releases', tag, 'signoffs'] })
    },
  })
}

/** Retirer n'est pas refuser : un avis rendu sur la mauvaise release doit pouvoir
 *  disparaître sans laisser un refus qui bloquerait la livraison. */
export function useClearSignoff(tag: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({ environment, role }: { environment: string; role: string }) =>
      request<void>(
        projectPath(`/releases/${encodeURIComponent(tag)}/signoffs/${encodeURIComponent(environment)}?role=${encodeURIComponent(role)}`),
        { method: 'DELETE' },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['releases', tag, 'signoffs'] })
    },
  })
}
