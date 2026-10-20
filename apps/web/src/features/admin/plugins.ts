import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request } from '../../lib/api'

/*
 * Les plugins, aux deux portées.
 *
 * Le registre vit dans le binaire : cet écran ne découvre rien, il montre ce que
 * cette version sait faire et ce que l'exploitation en a décidé. Les champs
 * eux-mêmes sont **décrits par le serveur** — nom, libellé, type, obligation,
 * sensibilité — plutôt que codés ici : les recopier ferait deux descriptions du
 * même formulaire, divergentes au premier champ ajouté.
 */

export type PluginField = {
  name: string
  label: string
  type: 'text' | 'url' | 'bool' | 'list'
  help?: string
  required?: boolean
  /** Un champ sensible n'est **jamais** rendu par l'API. L'écran n'en connaît que
   *  la présence, par `secrets_set`. */
  secret?: boolean
  placeholder?: string
}

/** Ce qu'un plugin apporte. Le libellé du genre est rendu par le serveur : la table
 *  des genres vit dans le registre, et la recopier ici la ferait diverger. */
export type PluginCapability = {
  kind: string
  kind_label: string
  name: string
  label: string
}

export type Plugin = {
  id: string
  label: string
  capabilities: PluginCapability[]
  summary: string
  per_project: boolean
  /** Le dashboard ne fonctionne pas sans : aucun interrupteur. */
  mandatory: boolean
  fields: PluginField[]
  enabled: boolean
  /** Ne porte que les champs **non sensibles**. */
  config: Record<string, string>
  /** Les champs sensibles qui portent une valeur — leur nom, pas leur contenu. */
  secrets_set: string[]
  /** Faux : le scellement n'est pas configuré côté serveur, et l'écran doit
   *  désactiver les champs sensibles plutôt que laisser saisir une valeur qui
   *  sera refusée. */
  secrets_configured: boolean
  updated_by?: string
}

export type PluginDraft = {
  enabled: boolean
  config: Record<string, string>
  /** L'effacement d'un champ sensible demande un geste distinct : un champ vide
   *  veut dire « inchangé », faute de quoi la clé serait perdue au premier
   *  enregistrement d'un autre champ. */
  cleared_secrets?: string[]
}

export function usePlugins() {
  return useQuery({
    queryKey: ['admin', 'plugins'],
    queryFn: () => request<{ plugins: Plugin[] }>('/admin/plugins'),
  })
}

export function useSavePlugin() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({ id, draft }: { id: string; draft: PluginDraft }) =>
      request<Plugin>(`/admin/plugins/${encodeURIComponent(id)}`, {
        method: 'PUT',
        body: JSON.stringify(draft),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['admin', 'plugins'] })
      // Les écrans de projet montrent ce que l'instance autorise : une extinction
      // globale doit les faire disparaître sans qu'on recharge la page.
      void queryClient.invalidateQueries({ queryKey: ['projects'] })
    },
  })
}

export function useProjectPlugins(project: string) {
  return useQuery({
    queryKey: ['projects', project, 'plugins'],
    queryFn: () =>
      request<{ plugins: Plugin[] }>(`/projects/${encodeURIComponent(project)}/plugins`),
    enabled: project !== '',
  })
}

export function useSaveProjectPlugin(project: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({ id, draft }: { id: string; draft: PluginDraft }) =>
      request<Plugin>(
        `/projects/${encodeURIComponent(project)}/plugins/${encodeURIComponent(id)}`,
        { method: 'PUT', body: JSON.stringify(draft) },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['projects', project] })
    },
  })
}

/**
 * Rend un projet à l'héritage de l'instance.
 *
 * Distinct d'une désactivation : « je n'ai rien de particulier à dire » n'est pas
 * « non ». Les confondre ferait qu'un projet, une fois écarté du réglage commun,
 * ne pourrait plus jamais le suivre à nouveau.
 */
export function useClearProjectPlugin(project: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (id: string) =>
      request<{ plugin: string }>(
        `/projects/${encodeURIComponent(project)}/plugins/${encodeURIComponent(id)}`,
        { method: 'DELETE' },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['projects', project] })
    },
  })
}

/*
 * Les familles du registre de pipelines qu'un projet retient.
 */

export type ProjectPipeline = {
  kind: string
  label: string
  /** La famille s'enchaîne après une construction réussie. Les autres se lancent
   *  à la main : les désactiver n'a pas le même effet, et l'écran le dit. */
  verification: boolean
  enabled: boolean
  /** Distingue « le projet a décidé » de « le projet n'a rien dit ». Sans lui,
   *  l'écran ne saurait pas si l'état affiché est un choix ou un défaut. */
  declared: boolean
  updated_by?: string
}

export function useProjectPipelines(project: string) {
  return useQuery({
    queryKey: ['projects', project, 'pipelines'],
    queryFn: () =>
      request<{ pipelines: ProjectPipeline[] }>(
        `/projects/${encodeURIComponent(project)}/pipelines`,
      ),
    enabled: project !== '',
  })
}

export function useSaveProjectPipeline(project: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: ({ kind, enabled }: { kind: string; enabled: boolean }) =>
      request<ProjectPipeline>(
        `/projects/${encodeURIComponent(project)}/pipelines/${encodeURIComponent(kind)}`,
        { method: 'PUT', body: JSON.stringify({ enabled }) },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['projects', project, 'pipelines'] })
    },
  })
}
