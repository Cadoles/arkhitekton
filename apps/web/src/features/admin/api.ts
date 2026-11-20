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
  /** Secret autorisant à pousser vers ce registre. Vide : celui du déploiement.
   *  Un **nom**, jamais un identifiant — le secret est provisionné par le
   *  déploiement, le dashboard se contente de le désigner. */
  registry_secret: string
  /** Pile dont la bibliothèque de référence est posée. Vide : le projet apporte
   *  ses propres pipelines, ce qui est parfaitement valide. */
  stack: string
  /** Nom du projet dans l'outil de suivi. Vide : aucun suivi, et ses versions se
   *  déclarent à la main.
   *
   *  Doit figurer dans le formulaire **et** être renvoyé avec le projet : le
   *  champ était absent des deux, et enregistrer un libellé effaçait donc la
   *  déclaration, arrêtant la synchronisation sans un mot. */
  tracker_project: string
  /** Fichier du dépôt où le lancement d'une release commite son tag. Vide : aucun. */
  version_file: string
  multi_stage: boolean
  targets: ProjectTarget[]
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
  /** L'identifiant unique du projet, dont ses Secrets tirent leur nom. */
  uid: string
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
   * Nom du projet dans les URL : `/projects/mstore/releases/1.8.0`. Vide à la
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
  registry_secret: string
  stack: string
  tracker_project: string
  /** Fichier du dépôt où le lancement d'une release commite son tag. Vide : aucun. */
  version_file: string
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

