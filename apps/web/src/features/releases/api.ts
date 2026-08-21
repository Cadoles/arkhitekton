import type { Health } from '../../lib/status'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request } from '../../lib/api'

/**
 * États d'une release, dans l'ordre du chemin construit (doc 04 §4.2).
 *
 * Une release **dérivée** — finale par re-tag — saute `building` et `verifying` :
 * rien n'est construit, donc rien ne s'exécute (B17).
 *
 * `ready` n'est pas promouvable : c'est `released` qui l'autorise, après décision
 * humaine. Les confondre ferait proposer une promotion avant validation.
 */
export type ReleaseState =
  | 'draft'
  | 'building'
  | 'verifying'
  | 'ready'
  | 'released'
  | 'failed'
  | 'cancelled'
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
  /** Le commit réellement construit. `git_ref` dit d'où la release est partie —
   *  une branche bouge, un commit non (B17). */
  git_commit?: string
  source_tag?: string
  /** La **dernière** construction a échoué. Une relance réussie l'efface. */
  build_failed?: boolean
  /** Environnements qui font tourner cette release. Vide dès qu'une autre y a
   *  été déployée depuis : la liste vient de ce que chaque environnement
   *  rapporte, pas de l'historique des promotions. */
  deployed_on?: string[]
  score?: { value: number; health: Health | 'na'; measured: boolean }
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
  /** Nom court du dépôt, tel que le registre l'attend. */
  repository: string
  /** Dépôt complet, registre et projet compris. Absent quand le projet n'est pas
   *  configuré : mieux vaut alors le nom court qu'une référence tronquée. */
  reference?: string
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
    // Tant qu'une exécution est en file ou en cours, la release est relue.
    //
    // C'est ce qui fait cesser l'animation d'un signal quand son pipeline
    // conclut : sans cela, la tuile resterait à pulser jusqu'au prochain
    // rechargement manuel, et un score recalculé ne s'afficherait pas. Le
    // sondage s'arrête de lui-même dès que plus rien ne tourne — il n'y a alors
    // plus rien à attendre.
    refetchInterval: (query) => {
      const jobs = query.state.data?.jobs ?? []
      const running = jobs.some((job) => job.state === 'queued' || job.state === 'running')
      return running ? 5000 : false
    },
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

export type Artifact = {
  id: number
  kind: string
  format: string
  size_bytes: number
  generated_by: string
  generated_at: string
}

export function useExports(tag: string | undefined) {
  return useQuery({
    queryKey: ['releases', tag, 'exports'],
    queryFn: () => request<{ artifacts: Artifact[] }>(`/releases/${encodeURIComponent(tag as string)}/exports`),
    enabled: Boolean(tag),
  })
}

/**
 * Demande de génération d'un livrable.
 *
 * La génération passe par la file comme le reste : elle y gagne
 * l'ordonnancement, la trace et la visibilité. Le livrable n'apparaît donc pas
 * immédiatement — il faut relire la liste, ce que l'invalidation déclenche.
 */
export function useGenerateExport(tag: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (payload: { kind: string; format: string }) =>
      request<{ job_id: number }>(`/releases/${encodeURIComponent(tag)}/exports`, {
        method: 'POST',
        body: JSON.stringify(payload),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['queue'] })
      void queryClient.invalidateQueries({ queryKey: ['releases', tag, 'exports'] })
    },
  })
}

/** URL de téléchargement. Le contenu transite par l'api, jamais par une URL
 *  pré-signée : ces objets portent la preuve d'une livraison, et une URL
 *  pré-signée est transmissible — elle échapperait au contrôle de rôle. */
export function downloadURL(tag: string, artifact: number): string {
  return `/api/v1/releases/${encodeURIComponent(tag)}/exports/${artifact}/download`
}

/*
 * Validation de recette.
 *
 * Doc 01 §1.2 décrit le sign-off comme la concaténation de deux moitiés : les
 * contrôles automatiques et la validation humaine. Seule la seconde vit ici — la
 * première est déjà chargée sous forme de résultats de tests, et la jointure se
 * fait dans le panneau, où les deux sont présentes.
 */

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
        `/releases/${encodeURIComponent(tag)}/signoffs`,
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
        `/releases/${encodeURIComponent(tag)}/signoffs/${encodeURIComponent(environment)}`,
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
        `/releases/${encodeURIComponent(tag)}/signoffs/${encodeURIComponent(environment)}?role=${encodeURIComponent(role)}`,
        { method: 'DELETE' },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['releases', tag, 'signoffs'] })
    },
  })
}

export type CreatedRelease = {
  release: { tag: string; kind: ReleaseKind; state: ReleaseState; image_version: string }
  /** Réserves émises par le serveur — RC aux tests non verts, re-tag non mis en
   *  file. La création a réussi malgré elles : les taire ferait croire à un
   *  succès sans condition. */
  warnings?: string[]
  retag_job_id?: number
}

/**
 * Création d'une release.
 *
 * Toute la validation vit côté serveur — cohérence tag/nature, jointure avec la
 * version Redmine (B17), permission `release:create:final` pour livrer. Le
 * formulaire ne pré-vérifie rien : dupliquer ces règles ici les ferait diverger,
 * et c'est précisément le message d'erreur du serveur qui explique le refus.
 */
export function useCreateRelease() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (payload: {
      tag: string
      kind: ReleaseKind
      redmine_version?: string
      git_ref?: string
      source_tag?: string
      tests_green?: boolean
    }) =>
      request<CreatedRelease>('/releases', { method: 'POST', body: JSON.stringify(payload) }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['releases'] })
      // La version gagne une release, et une finale met un re-tag en file.
      void queryClient.invalidateQueries({ queryKey: ['versions'] })
      void queryClient.invalidateQueries({ queryKey: ['queue'] })
    },
  })
}

/**
 * Validation d'une release : `ready` → `released`.
 *
 * C'est la décision humaine du cycle (doc 04 §4.2). Rien ne l'automatise, et
 * c'est le point — un pipeline vert dit que le code fonctionne, pas qu'on veut le
 * livrer. Tant qu'elle n'est pas prise, la release n'est ni promouvable ni
 * re-taguable en finale.
 */
export function useReleaseRelease(tag: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: () =>
      request<{ release: Release }>(`/releases/${encodeURIComponent(tag)}/release`, {
        method: 'POST',
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['releases'] })
      void queryClient.invalidateQueries({ queryKey: ['releases', tag] })
      // La liste des promouvables change : elle ne retient que les validées.
      void queryClient.invalidateQueries({ queryKey: ['environments'] })
    },
  })
}

/**
 * Relance la construction des images d'une release.
 *
 * Sert après un échec dont la cause est extérieure au code — clé de déploiement
 * mal déclarée, registre injoignable, nœud saturé. Créer une RC de plus pour
 * contourner un incident d'infrastructure ferait porter au numéro de version
 * l'historique des pannes du cluster.
 *
 * La référence Git n'est pas redemandée : c'est celle de la release, décidée à sa
 * création. La rouvrir permettrait de reconstruire un tag donné depuis un autre
 * commit, et deux images porteraient le même numéro sans être la même chose.
 */
/**
 * Relance d'une seule famille de vérification.
 *
 * Distincte de `useRebuild` : celle-ci reconstruit les images et rejoue tout,
 * celle-là rejoue un pipeline sur l'image déjà publiée. L'état de la release ne
 * bouge pas — une relance ne dit rien de neuf tant qu'elle n'a pas conclu.
 */
export function useVerify(tag: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (kind: string) =>
      request<{ kind: string; job_id: number; revision: string }>(
        `/releases/${encodeURIComponent(tag)}/verify/${encodeURIComponent(kind)}`,
        { method: 'POST' },
      ),
    onSuccess: () => {
      // La release porte la liste des exécutions, d'où l'écran déduit qu'un
      // pipeline tourne : sans cette invalidation, le bouton resterait au repos
      // alors que le run est parti.
      void queryClient.invalidateQueries({ queryKey: ['releases', tag] })
      void queryClient.invalidateQueries({ queryKey: ['releases', tag, 'tests'] })
      void queryClient.invalidateQueries({ queryKey: ['queue'] })
    },
  })
}

export function useRebuild(tag: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: () =>
      request<{ release: Release; build_job_id: number }>(
        `/releases/${encodeURIComponent(tag)}/build`,
        { method: 'POST' },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['releases', tag] })
      void queryClient.invalidateQueries({ queryKey: ['releases'] })
      void queryClient.invalidateQueries({ queryKey: ['queue'] })
    },
  })
}

/*
 * Note de release : les demandes Redmine que la release apporte.
 *
 * Deux sous-ensembles à l'affichage — nouveautés et corrections — décidés par le
 * tracker de la demande et non par son statut : c'est la nature de ce qui a été
 * fait qui range la ligne, pas son avancement.
 */
export type NoteEntry = {
  redmine_id: number
  subject?: string
  tracker?: string
  status?: string
  /** Range la ligne : les anomalies aux corrections, le reste aux nouveautés. */
  anomaly: boolean
  /** Faux tant que la synchronisation Redmine n'a pas vu la demande. La ligne
   *  reste affichée : la masquer ferait disparaître un ajout manuel. */
  known: boolean
  source: 'auto' | 'manual'
  pull_request?: string
  added_by: string
  url?: string
}

export function useNotes(tag: string | undefined) {
  return useQuery({
    queryKey: ['releases', tag, 'notes'],
    queryFn: () =>
      // `available` est le vivier de rattachement : les demandes de la version
      // moins celles déjà dans la note. Rendu avec elle plutôt que par un second
      // appel — les deux se lisent ensemble, et l'écran n'a pas à composer une
      // différence que le serveur connaît.
      request<{ notes: NoteEntry[]; available: NoteEntry[] }>(
        `/releases/${encodeURIComponent(tag as string)}/notes`,
      ),
    enabled: Boolean(tag),
  })
}

export function useAddNote(tag: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (redmineID: number) =>
      request<{ redmine_id: number }>(
        `/releases/${encodeURIComponent(tag)}/notes/${redmineID}`,
        { method: 'POST' },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['releases', tag, 'notes'] })
    },
  })
}

export function useRemoveNote(tag: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (redmineID: number) =>
      request<void>(`/releases/${encodeURIComponent(tag)}/notes/${redmineID}`, {
        method: 'DELETE',
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['releases', tag, 'notes'] })
    },
  })
}
