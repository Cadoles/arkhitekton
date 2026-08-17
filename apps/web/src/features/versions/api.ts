import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request } from '../../lib/api'

/**
 * Les seaux de la projection des statuts Redmine, dans l'ordre d'avancement.
 *
 * `client-test` est l'attente de validation par le client : le développement est
 * fini, la recette non. **Il ne compte pas comme terminé** — il entre au
 * dénominateur de la complétion, jamais au numérateur.
 *
 * `rejected` est hors de l'avancement : ni avant ni après, hors périmètre.
 */
export type Bucket = 'todo' | 'doing' | 'client-test' | 'done' | 'rejected'

/**
 * Les deux mesures de complétion, calculées côté serveur (doc 04 §4.5).
 *
 * `ratio` est nul quand rien n'est comptable : afficher zéro pour cent laisserait
 * croire qu'une version vide est en retard.
 */
export type Completion = {
  counted: number
  done: number
  ratio: number | null
  weighted_ratio: number | null
}

export type Version = {
  id: number
  redmine_id?: number
  name: string
  status: string
  due_date: string | null
  synced_at: string
  /** Une release s'y rattache, mais aucune version Redmine n'a été trouvée. */
  pending: boolean
  completion: Completion
  /** Compté à part : une version peut être complète et porter des anomalies. */
  open_anomalies: number
  releases: number
  url?: string
}

export type Issue = {
  redmine_id: number
  tracker: string
  subject: string
  status_name: string
  bucket: Bucket
  priority?: string
  assignee?: string
  done_ratio: number
  is_closed: boolean
  anomaly: boolean
  url?: string
}

export type VersionRelease = {
  tag: string
  kind: string
  state: string
  image_version: string
  created_at: string
}

/**
 * État de la synchronisation.
 *
 * `last_error` renseigné alors que `last_ok` l'est aussi signifie « données valides
 * mais vieillissantes », ce qui n'appelle pas la même réaction qu'une absence
 * totale de données (doc 08 §8.2).
 */
export type SyncState = {
  last_run: string | null
  last_ok: string | null
  last_error?: string
  ever_ran: boolean
}

export function useVersions() {
  return useQuery({
    queryKey: ['versions'],
    queryFn: () => request<{ versions: Version[]; sync: SyncState }>('/versions'),
  })
}

export function useVersion(id: string | undefined) {
  return useQuery({
    queryKey: ['versions', id],
    queryFn: () =>
      request<{
        version: Version
        buckets: Record<Bucket, Issue[]>
        anomalies: Issue[]
        releases: VersionRelease[]
      }>(`/versions/${encodeURIComponent(id as string)}`),
    enabled: Boolean(id),
  })
}

export type SyncAdmin = {
  /** Faux sans clé Redmine : l'écran doit dire « non configuré » et non « jamais
   *  synchronisé », qui ferait chercher une panne. */
  configured: boolean
  sync: SyncState
  unmapped: { status_name: string; issues: number }[]
  ambiguous: { normalized: string; names: string[] }[]
  pending: string[]
  mapping: Record<string, Bucket>
}

export function useSyncAdmin() {
  return useQuery({
    queryKey: ['admin', 'sync'],
    queryFn: () => request<SyncAdmin>('/admin/sync'),
  })
}

export function useForceResync() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: () =>
      request<{ message: string }>('/admin/sync/redmine/resync', { method: 'POST' }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['admin', 'sync'] })
    },
  })
}

/**
 * Déclare la projection d'un statut.
 *
 * Le serveur réapplique la règle aux demandes déjà enregistrées dans le même geste
 * et rend le nombre déplacé. Sans cela, la nouvelle règle ne vaudrait que pour les
 * demandes modifiées ensuite dans Redmine, et l'écran mélangerait deux règles.
 */
export function useSetStatusBucket() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({ status, bucket }: { status: string; bucket: Bucket }) =>
      request<{ issues_moved: number }>(
        `/admin/sync/redmine/mapping/${encodeURIComponent(status)}`,
        { method: 'PUT', body: JSON.stringify({ bucket }) },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['admin', 'sync'] })
      void queryClient.invalidateQueries({ queryKey: ['versions'] })
    },
  })
}

/**
 * Références du dépôt de l'application, pour le champ git_ref.
 *
 * `configured` faux : pas de jeton Gitea côté serveur, le formulaire retombe sur
 * la saisie libre et le dit. `truncated` vrai : le dépôt porte plus de références
 * que la borne, la liste est un extrait — la saisie libre reste possible, une
 * référence absente de la liste n'est pas une référence inexistante.
 */
export type ForgeRefs = {
  configured: boolean
  default_branch?: string
  branches: { name: string; sha?: string }[]
  tags: { name: string; sha?: string }[]
  truncated?: boolean
}

export function useForgeRefs(enabled: boolean) {
  return useQuery({
    queryKey: ['forge', 'refs'],
    queryFn: () => request<ForgeRefs>('/forge/refs'),
    enabled,
    // Une minute : les branches bougent au rythme des développeurs, pas de la
    // frappe dans un formulaire. Rappeler la forge à chaque focus serait du bruit.
    staleTime: 60_000,
    // Un échec — forge injoignable, jeton révoqué — ne se réessaie pas en boucle :
    // le formulaire affiche la cause et la saisie libre reste ouverte.
    retry: false,
  })
}
