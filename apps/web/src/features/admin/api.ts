import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request } from '../../lib/api'
import type { Role } from '../auth/session'

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
  'release.create': 'Création de brouillon de release',
  'release.release': 'Validation de release',
  'release.rebuild': 'Reconstruction de release',
  'release.verify': 'Relance de vérification',
  'release.run': 'Pipeline lancé sur une release',
  'release.note.add': 'Ajout à la note de release',
  'release.note.remove': 'Retrait de la note de release',
  'release.signoff': 'Avis de recette',
  'release.signoff.clear': "Retrait d'un avis de recette",
  'release.launch': 'Lancement de release',
  'release.draft.update': 'Modification de brouillon',
  'release.draft.delete': 'Suppression de brouillon',
  'release.note.detect': 'Détection de la note',
  'release.promote': 'Promotion',
  'release.actions': 'Actions manuelles',
  'job.enqueue': 'Mise en file',
  'job.cancel': 'Annulation',
  'artifact.generate': 'Génération de livrable',
  'bench.baseline.pin': 'Épinglage de baseline',
  'role.grant': 'Attribution de rôle',
  'role.revoke': 'Retrait de rôle',
  'group.grant': 'Rôle donné à un groupe',
  'group.revoke': "Retrait du rôle d'un groupe",
  'agent.authorize': "Autorisation d'un agent IA",
  'agent.revoke': "Révocation d'un agent IA",
  'queue.limit': "Plafond de file d'attente",
  'score.weight': 'Poids dans le score',
  'project.set': 'Déclaration de projet',
  'project.remove': 'Retrait de projet',
  'project.probe': "Vérification d'accès au dépôt",
  'version.declare': 'Déclaration de version',
  'automation.save': "Enregistrement d'automatisation",
  'automation.disable': "Désactivation d'automatisation",
  'reaction.save': 'Enregistrement de réaction',
  'reaction.disable': 'Désactivation de réaction',
  'reaction.delete': 'Suppression de réaction',
  'schedule.save': 'Enregistrement de planification',
  'schedule.delete': 'Suppression de planification',
  'capability.binding': 'Liaison de capacité',
  'capability.unbinding': 'Retrait de liaison de capacité',
  'plugin.settings': 'Réglages de plugin',
  'plugin.project.settings': "Réglages de plugin d'un projet",
  'plugin.project.clear': "Retrait des réglages de plugin d'un projet",
  'plugin.update.requested': 'Demande de mise à jour de plugin',
  'plugin.installation.requested': "Demande d'installation de plugin",
  'plugin.installed': 'Installation de plugin',
  'plugin.installation.failed': "Échec d'installation de plugin",
  'plugin.activated': 'Activation de version de plugin',
  'plugin.version.removed': 'Retrait de version de plugin',
  'plugin.uninstalled': 'Désinstallation de plugin',
  'plugin.purge.requested': 'Demande de purge de plugin',
  'plugin.purged': 'Purge de plugin',
  'plugin.purge.failed': 'Échec de purge de plugin',
}

/*
 * Les dépôts pilotés par le dashboard.
 *
 * Ils vivaient dans la configuration : en déclarer un de plus demandait de
 * modifier un ConfigMap, donc de reconstruire et de redéployer. Rien ne disait,
 * depuis l'interface, quels dépôts le dashboard croyait piloter — et surtout,
 * rien ne permettait de vérifier qu'il y avait accès.
 */

export type Project = {
  id: number
  /** Nom du projet dans les URL, dérivé du dépôt à la création. */
  slug: string
  repository: string
  label: string
  forge_url: string
  /** Lien vers le dépôt. Vide si la forge n'est pas connue. */
  url: string
  /** Absent : aucun test n'a eu lieu. État distinct d'un test qui a échoué. */
  checked_at?: string
  check_ok?: boolean
  check_note?: string
  /** Moitié **publique** de la clé propre au projet. Absente tant que le worker
   *  ne l'a pas posée. La moitié privée n'apparaît jamais, ni ici ni ailleurs. */
  deploy_key?: string
  deploy_key_created_at?: string
  /** La clé ouvre vraiment le dépôt : le test d'accès l'a trouvée dans ses clés.
   *  Faux, les pipelines du projet refusent de partir. */
  deploy_key_active: boolean
  /** Pourquoi le worker n'a pas pu poser la clé. Absent dès qu'il y arrive. */
  deploy_key_error?: string
  /** Ce que la dernière vérification a constaté de la clé, surtout quand elle n'a
   *  pas pu conclure. */
  key_note?: string
  /** La page de la forge qui liste les clés de déploiement du dépôt : là où coller
   *  celle du projet. */
  keys_url?: string
  /** L'identifiant unique du projet, dont ses Secrets tirent leur nom. */
  uid: string
  created_by: string
}

type ProjectsResponse = {
  projects: Project[]
  /** Moitié publique de la clé que les pipelines présentent au clone. */
  deploy_key: string
  forge_url: string
  /** Faux quand la forge n'est pas configurée : le test ne peut pas conclure. */
  probe_configured: boolean
}

/** Ce qu'un formulaire envoie. Le dépôt n'est modifiable qu'à la déclaration. */
export type ProjectDraft = {
  /**
   * Nom du projet dans les URL : `/projects/shop/releases/1.8.0`. Vide à la
   * création, l'API le dérive du dépôt. Modifiable ensuite — ces adresses
   * finissent en favori, et un nom qu'on ne peut pas corriger est un nom subi.
   */
  slug?: string
  repository: string
  label: string
  forge_url: string
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
        ? request<{ id: number; slug?: string }>('/projects', {
            method: 'POST',
            body: JSON.stringify(draft),
          })
        : request<{ id: number; slug?: string }>(`/projects/${id}`, {
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
      request<ProbeResult>(`/projects/${id}/probe`, { method: 'POST' }),
    onSuccess: () => {
      // Le constat est enregistré côté serveur : le relire est ce qui met la
      // liste à jour, plutôt que de tenir un état parallèle qui divergerait.
      // C'est aussi ce qui fait remonter le sort de la clé, que ce même test
      // vient de constater.
      void queryClient.invalidateQueries({ queryKey: ['projects'] })
    },
  })
}

/**
 * Ce qu'un test d'accès a constaté.
 *
 * Deux constats distincts : le dashboard **lit-il** ce dépôt, et la clé de
 * déploiement du projet y **est-elle posée** ? Le premier se fait par jeton et en
 * HTTPS, le second lit ce que la forge déclare de ses clés.
 */
export type ProbeResult = {
  ok: boolean
  note: string
  /**
   * Trois états, pas deux. `null` veut dire « la question n'a pas pu être
   * posée » — jeton sans droit d'administration sur le dépôt, ou clé pas encore
   * produite — et ce n'est pas « la clé est absente ».
   */
  key_present: boolean | null
  key_note: string
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

