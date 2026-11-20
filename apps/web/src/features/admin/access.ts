import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request } from '../../lib/api'
import type { Role } from '../auth/session'

/*
 * Qui peut se servir du dashboard : les personnes, et les rôles donnés aux
 * groupes du fournisseur d'identité (les organisations et équipes de la forge).
 *
 * Ce qu'une personne peut faire est résolu par le serveur, selon la même règle
 * qu'à chaque requête : son propre rôle l'emporte sur ceux de ses groupes à une
 * même portée, sinon le plus élevé de ses groupes ; puis le rôle d'un projet
 * l'emporte sur celui de l'instance.
 */

export type Grant = {
  /** Absent : le rôle vaut pour toute l'instance. */
  project?: string
  project_label?: string
  role: Role
  granted_by: string
  granted_at: string
  /** Attribué par la configuration de démarrage. */
  bootstrap?: boolean
}

export type Effective = {
  project?: string
  project_label?: string
  /** Vide : rien ne s'applique à cette portée. */
  role: Role | ''
  /** Le groupe dont vient le rôle ; absent quand c'est celui de la personne. */
  source?: string
}

export type Person = {
  subject: string
  username?: string
  email?: string
  groups: string[]
  /** Absent pour quelqu'un qui ne s'est jamais connecté. */
  last_seen?: string
  sessions: number
  /** Ses propres rôles. */
  roles: Grant[]
  global: Effective
  /** Les projets où ce qui s'applique diffère du global. */
  projects: Effective[]
  /** Connecté, sans aucun accès : en attente. */
  pending: boolean
}

export type GroupAccess = { group: string; members: number; roles: Grant[] }

export type RoleCatalogue = {
  role: Role
  permissions: string[]
  /** Promouvoir vers un environnement non sensible, sensible. */
  deploy: { open: boolean; sensitive: boolean }
}

export function usePeople() {
  return useQuery({
    queryKey: ['admin', 'people'],
    queryFn: () => request<{ people: Person[] }>('/admin/people'),
  })
}

export function useGroups() {
  return useQuery({
    queryKey: ['admin', 'groups'],
    queryFn: () => request<{ groups: GroupAccess[]; known: GroupAccess[] }>('/admin/groups'),
  })
}

export function useRoleCatalogue() {
  return useQuery({
    queryKey: ['admin', 'roles'],
    queryFn: () => request<{ catalogue: RoleCatalogue[] }>('/admin/roles'),
    staleTime: Infinity,
  })
}

function useAdminMutation<Payload>(call: (payload: Payload) => Promise<unknown>) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: call,
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['admin'] })
    },
  })
}

/** Donne un rôle à une personne, partout ou dans un projet. Ses sessions sont
 *  fermées : le changement prend effet tout de suite. */
export function useGrantRole() {
  return useAdminMutation((payload: { subject: string; username?: string; role: Role; project?: string }) =>
    request('/admin/roles', { method: 'POST', body: JSON.stringify(payload) }),
  )
}

export function useRevokeRole() {
  return useAdminMutation(({ subject, project }: { subject: string; project?: string }) =>
    request(`/admin/roles/${encodeURIComponent(subject)}${project ? `?project=${encodeURIComponent(project)}` : ''}`, {
      method: 'DELETE',
    }),
  )
}

/** Donne un rôle à un groupe. Il s'applique à la requête suivante de ses membres. */
export function useSetGroupRole() {
  return useAdminMutation(({ group, ...payload }: { group: string; role: Role; project?: string }) =>
    request(`/admin/groups/${encodeURIComponent(group)}/roles`, { method: 'PUT', body: JSON.stringify(payload) }),
  )
}

export function useClearGroupRole() {
  return useAdminMutation(({ group, project }: { group: string; project?: string }) =>
    request(
      `/admin/groups/${encodeURIComponent(group)}/roles${project ? `?project=${encodeURIComponent(project)}` : ''}`,
      { method: 'DELETE' },
    ),
  )
}

/** Ce que chaque permission permet, en français, dans l'ordre des rôles. Le
 *  serveur décide qui les a ; cette liste ne fait que les nommer. */
export const PERMISSIONS: { permission: string; label: string }[] = [
  { permission: 'read', label: 'Consulter' },
  { permission: 'release:export', label: 'Télécharger un livrable' },
  { permission: 'pipeline:run', label: 'Lancer un pipeline' },
  { permission: 'pipeline:cancel', label: 'Annuler un pipeline' },
  { permission: 'release:create', label: 'Créer et lancer une release' },
  { permission: 'release:edit', label: 'Modifier une release (notes, actions)' },
  { permission: 'release:create:final', label: 'Créer une release finale, valider' },
]
