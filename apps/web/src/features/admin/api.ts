import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request } from '../../lib/api'
import type { Role } from '../auth/session'

export type UserRole = {
  /** Identifiant du fournisseur d'identité. C'est lui qui porte l'autorisation :
   *  un nom d'utilisateur change, un sujet non. */
  subject: string
  username?: string
  role: Role
  /** `bootstrap` pour les comptes promus par la configuration au démarrage.
   *  Les retirer sans toucher à la configuration les fait réapparaître. */
  granted_by: string
  granted_at: string
  /** Absent pour un compte habilité qui ne s'est jamais connecté — c'est ainsi
   *  qu'un rôle attribué à un identifiant erroné se reconnaît. */
  last_seen?: string
  sessions: number
  /** Portée du rôle. Absente quand il vaut partout — le cas courant. Une même
   *  personne peut donc apparaître plusieurs fois : une ligne globale, une par
   *  projet où une exception a été posée. */
  project?: string
  project_label?: string
}

export type RoleCatalogue = { role: Role; permissions: string[] }

export function useRoles() {
  return useQuery({
    queryKey: ['admin', 'roles'],
    queryFn: () => request<{ roles: UserRole[]; catalogue: RoleCatalogue[] }>('/admin/roles'),
  })
}

export function useGrantRole() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (payload: { subject: string; username: string; role: Role; project?: string }) =>
      request<{ subject: string; sessions_closed: boolean }>('/admin/roles', {
        method: 'POST',
        body: JSON.stringify(payload),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['admin'] })
    },
  })
}

export function useRevokeRole() {
  const queryClient = useQueryClient()

  return useMutation({
    // La portée accompagne le retrait : « retirer le rôle de quelqu'un » n'a plus
    // de sens unique depuis qu'il peut en avoir un par projet.
    mutationFn: ({ subject, project }: { subject: string; project?: string }) =>
      request<{ subject: string }>(
        `/admin/roles/${encodeURIComponent(subject)}` +
          (project ? `?project=${encodeURIComponent(project)}` : ''),
        { method: 'DELETE' },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['admin'] })
    },
  })
}

export type AuditEntry = {
  id: number
  actor: string
  action: string
  target?: string
  payload?: Record<string, unknown>
  at: string
}

export function useAudit(target: string) {
  return useQuery({
    queryKey: ['admin', 'audit', target],
    queryFn: () =>
      request<{ entries: AuditEntry[] }>(
        `/admin/audit?limit=100${target ? `&target=${encodeURIComponent(target)}` : ''}`,
      ),
  })
}

export const ROLE_LABEL: Record<Role, string> = {
  viewer: 'Lecteur',
  operator: 'Opérateur',
  releaser: 'Responsable de livraison',
  admin: 'Administrateur',
}

/** Ce que chaque action d'audit désigne, en français. Les clés sont celles que
 *  le serveur écrit — une action inconnue s'affiche telle quelle plutôt que
 *  disparaître. */
export const ACTION_LABEL: Record<string, string> = {
  'release.create': 'Création de release',
  'release.promote': 'Promotion',
  'release.actions': 'Actions manuelles',
  'job.enqueue': 'Mise en file',
  'job.cancel': 'Annulation',
  'artifact.generate': 'Génération de livrable',
  'bench.baseline.pin': 'Épinglage de baseline',
  'role.grant': 'Attribution de rôle',
  'role.revoke': 'Retrait de rôle',
}

/*
 * Les dépôts pilotés par le dashboard.
 *
 * Ils vivaient dans la configuration : en déclarer un de plus demandait de
 * modifier un ConfigMap, donc de reconstruire et de redéployer. Rien ne disait,
 * depuis l'interface, quels dépôts le dashboard croyait piloter — et surtout,
 * rien ne permettait de vérifier qu'il y avait accès.
 */

/**
 * Une étape constructible et le dépôt d'image qu'elle alimente.
 *
 * `stage` est vide sur un Dockerfile à image unique, qui n'a pas de cible.
 * `reference` est composée par le serveur — registre compris — et n'est pas
 * renvoyée : la recomposer côté navigateur la ferait diverger de ce que le
 * pipeline recevra.
 */
export type ProjectTarget = {
  stage: string
  image: string
  /**
   * Dockerfile propre à cette image, quand il diffère de celui du projet. Un
   * dépôt peut en porter un par image — `Dockerfile.dev`, `Dockerfile.prod` —
   * plutôt qu'un seul à étapes. Vide, celui du projet s'applique.
   */
  dockerfile?: string
  reference?: string
  /**
   * Vrai si un re-tag propage cette image vers les environnements. Faux pour une
   * image que la construction publie sans qu'aucun environnement du flux ne la
   * déploie — l'image de développement : la re-taguer en préproduction
   * reviendrait à l'annoncer comme livrable.
   */
  promote: boolean
}

export type Project = {
  id: number
  /** Nom du projet dans les URL, dérivé du dépôt à la création. */
  slug: string
  repository: string
  label: string
  forge_url: string
  /** Lien vers le dépôt. Vide si la forge n'est pas connue. */
  url: string
  dockerfile: string
  build_context: string
  /** Registre et projet où publier, sans le nom d'image. */
  image_registry: string
  multi_stage: boolean
  targets: ProjectTarget[]
  /** Absent : aucun test n'a eu lieu. État distinct d'un test qui a échoué. */
  checked_at?: string
  check_ok?: boolean
  check_note?: string
  /** Moitié **publique** de la clé propre au projet. Absente tant que le worker
   *  ne l'a pas posée : le projet emploie alors la clé partagée. La moitié
   *  privée n'apparaît jamais, ni ici ni ailleurs. */
  deploy_key?: string
  deploy_key_created_at?: string
  /** La clé ouvre vraiment le dépôt : quelqu'un l'a collée dans ses réglages.
   *  Faux, les pipelines montent la clé partagée — qui, elle, fonctionne. */
  deploy_key_active: boolean
  created_by: string
}

type ProjectsResponse = {
  projects: Project[]
  /** Moitié publique de la clé que les pipelines présentent au clone. */
  deploy_key: string
  forge_url: string
  /** Registre proposé à la déclaration : celui où publient les projets existants. */
  image_registry: string
  /** Faux quand la forge n'est pas configurée : le test ne peut pas conclure. */
  probe_configured: boolean
}

/** Ce qu'un formulaire envoie. Le dépôt n'est modifiable qu'à la déclaration. */
export type ProjectDraft = {
  /**
   * Nom du projet dans les URL : `/projects/mse/releases/1.8.0`. Vide à la
   * création, l'API le dérive du dépôt. Modifiable ensuite — ces adresses
   * finissent en favori, et un nom qu'on ne peut pas corriger est un nom subi.
   */
  slug?: string
  repository: string
  label: string
  forge_url: string
  dockerfile: string
  build_context: string
  image_registry: string
  multi_stage: boolean
  targets: ProjectTarget[]
}

export function useProjects() {
  return useQuery({
    queryKey: ['projects'],
    queryFn: () => request<ProjectsResponse>('/projects'),
  })
}

export function useSaveProject() {
  const queryClient = useQueryClient()

  return useMutation({
    // Un identifiant : c'est une mise à jour. Sans : une déclaration. Le dépôt
    // n'est pas modifiable — le changer ferait d'un projet un autre, et les
    // constats d'accès porteraient sur ce qui n'a pas été testé.
    mutationFn: ({ id, draft }: { id?: number; draft: ProjectDraft }) =>
      id === undefined
        ? request<{ id: number }>('/projects', {
            method: 'POST',
            body: JSON.stringify(draft),
          })
        : request<{ id: number }>(`/projects/${id}`, {
            method: 'PUT',
            body: JSON.stringify(draft),
          }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['projects'] })
    },
  })
}

export function useDeleteProject() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (id: number) => request<{ id: number }>(`/projects/${id}`, { method: 'DELETE' }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['projects'] })
    },
  })
}

export function useProbeProject() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (id: number) =>
      request<{ ok: boolean; note: string }>(`/projects/${id}/probe`, { method: 'POST' }),
    onSuccess: () => {
      // Le constat est enregistré côté serveur : le relire est ce qui met la
      // liste à jour, plutôt que de tenir un état parallèle qui divergerait.
      void queryClient.invalidateQueries({ queryKey: ['projects'] })
    },
  })
}

/*
 * Demande le remplacement de la clé d'un projet.
 *
 * Une **demande** : l'API n'a pas d'accès au cluster, et c'est le worker qui
 * pose. L'ancienne clé reste valable jusque-là, si bien qu'une rotation ne casse
 * aucun clone en cours — mais l'écran doit dire « demandée », pas « faite ».
 */
export function useRotateDeployKey() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (id: number) =>
      request<{ requested: boolean }>(`/projects/${id}/deploy-key`, { method: 'POST' }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['projects'] })
    },
  })
}

/*
 * Déclare que la clé d'un projet ouvre son dépôt.
 *
 * Le seul geste de la chaîne que le dashboard ne peut pas faire : coller la
 * moitié publique dans les réglages du dépôt appartient à un humain. Tant qu'il
 * ne l'a pas dit, les pipelines montent la clé partagée.
 */
export function useActivateDeployKey() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({ id, active }: { id: number; active: boolean }) =>
      request<{ active: boolean }>(`/projects/${id}/deploy-key/active`, {
        method: 'POST',
        body: JSON.stringify({ active }),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['projects'] })
    },
  })
}
