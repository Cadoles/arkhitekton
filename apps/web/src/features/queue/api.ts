import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request } from '../../lib/api'
import { currentProject } from '../../lib/project'

export type JobState = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'

/**
 * Motifs d'attente, tels que la base les calcule.
 *
 * Deux seulement depuis que chaque type a sa file : elle est pleine, ou le
 * plafond global l'est. Le modèle précédent en avait trois — `class-busy`,
 * `capacity`, `draining` — dont un, le drainage, qu'il fallait expliquer en
 * deux phrases parce que rien à l'écran ne le laissait deviner.
 */
export type QueueReason = 'kind-busy' | 'capacity'

export type Job = {
  id: number
  kind: string
  label: string
  priority: number
  state: JobState
  requested_by: string
  requested_at: string
  started_at?: string
  attempt: number
  queue_reason?: QueueReason
  run_name?: string
}

/** Une file : son plafond, son occupation, ses jobs dans l'ordre du service. */
export type Queue = {
  kind: string
  label: string
  /** Plafond effectif : le réglage s'il existe, celui du catalogue sinon. */
  max_concurrent: number
  /** Plafond du catalogue, pour dire « réglé à 2, par défaut 5 ». */
  default_max_concurrent: number
  overridden: boolean
  /** Une file constatée hors catalogue : une tâche locale, qui n'a pas de pipeline. */
  known: boolean
  running: number
  waiting: number
  jobs: Job[]
}

/** Un champ du formulaire de lancement, tel que le catalogue le déclare. */
export type Input = {
  name: string
  required: boolean
  /**
   * Nature de la valeur attendue, qui nomme la liste à proposer. Absente, le
   * champ reste libre : le dashboard ne sait pas énumérer ses valeurs.
   */
  suggest?: string
}

export type Kind = {
  kind: string
  label: string
  /** Tous les paramètres saisis, requis ou non. */
  inputs: Input[]
}

/** Une valeur proposée : ce qui part, et ce qui se lit. */
export type Option = { value: string; label?: string }

type QueueResponse = {
  queues: Queue[]
  kinds: Kind[]
  /**
   * Listes proposées, par nature. Les références Git n'y sont pas : elles se
   * lisent en direct chez la forge, et les recharger avec la file les ferait
   * payer toutes les deux secondes.
   */
  suggestions: Record<string, Option[]>
  max_concurrent: number
}

export function useQueue() {
  return useQuery({
    queryKey: ['queue', currentProject()],
    // Le projet accompagne la lecture : la file elle-même est celle du cluster,
    // mais ses suggestions listent des releases, qui n'existent que dans un
    // projet. Sans lui, les champs concernés restent en saisie libre.
    queryFn: () =>
      request<QueueResponse>(`/queue?project=${encodeURIComponent(currentProject())}`),
    // La file bouge en permanence, et son intérêt est précisément de dire ce qui
    // se passe maintenant. Deux secondes suivent le rythme de l'ordonnanceur.
    refetchInterval: 2000,
  })
}

export function useLaunch() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (payload: { kind: string; params: Record<string, string> }) =>
      request<{ id: number }>('/queue', { method: 'POST', body: JSON.stringify(payload) }),
    onSuccess: () => {
      // La file est rafraîchie sans attendre le prochain intervalle : voir son
      // job apparaître est ce qui confirme que le lancement a été pris.
      void queryClient.invalidateQueries({ queryKey: ['queue', currentProject()] })
    },
  })
}

export function useCancel() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (id: number) => request<{ state: string }>(`/queue/${id}/cancel`, { method: 'POST' }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['queue', currentProject()] })
    },
  })
}

/**
 * Réglage du plafond d'une file.
 *
 * `max_concurrent` à `null` rend la file à son plafond de catalogue. Deux verbes
 * pour deux intentions : régler est un `PUT`, revenir au défaut un `DELETE` —
 * envoyer zéro pour dire « comme avant » aurait confondu « pas de limite » et
 * « limite absente ».
 */
export function useSetLimit() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({ kind, max }: { kind: string; max: number | null }) =>
      max === null
        ? request<{ kind: string }>(`/queue/limits/${encodeURIComponent(kind)}`, { method: 'DELETE' })
        : request<{ kind: string }>(`/queue/limits/${encodeURIComponent(kind)}`, {
            method: 'PUT',
            body: JSON.stringify({ max_concurrent: max }),
          }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['queue', currentProject()] })
    },
  })
}

/**
 * Explication d'une attente, en français.
 *
 * Le texte dit **ce qui bloque**, pas seulement qu'il y a blocage : une file qui
 * n'explique pas son attente engendre systématiquement des relances inutiles.
 */
export function explainWait(job: Job, queue: Queue | undefined): string {
  switch (job.queue_reason) {
    case 'kind-busy':
      return queue
        ? `La file ${queue.label} est pleine — ${queue.max_concurrent} en parallèle au maximum.`
        : 'La file de ce type est pleine.'
    case 'capacity':
      // Le plafond global prime sur celui de la file, et régler la file n'y
      // changerait rien : le dire évite de chercher la place au mauvais endroit.
      return 'Le plafond global d’exécutions simultanées est atteint, toutes files confondues.'
    default:
      return 'En attente de son tour.'
  }
}
