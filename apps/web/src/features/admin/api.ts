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
    mutationFn: (payload: { subject: string; username: string; role: Role }) =>
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
    mutationFn: (subject: string) =>
      request<{ subject: string }>(`/admin/roles/${encodeURIComponent(subject)}`, { method: 'DELETE' }),
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
