import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request } from '../../lib/api'

/*
 * Les réactions déclarées d'un projet, et le catalogue qui permet de les écrire.
 *
 * Aucune liste n'est codée ici. Les faits écoutables, les gestes disponibles et
 * les conditions viennent du serveur : une liste écrite dans l'interface
 * divergerait du binaire au premier ajout, et l'écart se verrait par un geste
 * qu'on peut choisir et que rien n'exécute.
 */

/** Un geste : une action, ou un pipeline. */
export type Geste = {
  kind: 'action' | 'pipeline'
  ref: string
  label?: string
  /** `success` par défaut. Relatif à l'issue de l'étape précédente. */
  when?: 'success' | 'failure' | 'always'
  params?: Record<string, string>
}

/** Une étape : des gestes exécutés en parallèle.
 *
 *  `role` n'a de sens que sur l'automatisation d'une release : c'est la
 *  conclusion d'une étape à rôle qui fait avancer son état. Une réaction qui en
 *  porterait un est refusée — elle ne vise aucune release en particulier. */
export type Stage = { label?: string; role?: string; gestes: Geste[] }

export type Plan = { stages: Stage[] }

/** Un rôle d'étape, et ce qu'il exige de ses gestes. */
export type RoleChoice = {
  role: string
  label: string
  /** Le produit qu'un geste doit déclarer pour y entrer — `image`, `results`. */
  requires: string
  /** Ce que la conclusion de cette étape fait à la release. */
  advances: string
}

/** L'automatisation d'une release : ce qu'un projet construit, puis vérifie. */
export type Automation = {
  plan: Plan
  /** Faux quand personne n'a jamais rien déclaré. Distinct d'un plan vide, qui
   *  dit « ce projet ne construit plus » : seul le premier sera repris depuis les
   *  cibles du projet au prochain démarrage du worker. */
  declared: boolean
  updated_by?: string
}

export type Reaction = {
  id: number
  name: string
  event_type: string
  enabled: boolean
  /** L'appariement sur la charge du fait. Vide : part sur tout fait de ce type. */
  match: Record<string, string>
  plan: Plan
  /** Vrai quand le fait écouté n'est plus produit — plugin éteint, ou type retiré
   *  d'une version. La réaction reste lisible et ne se déclenche plus. */
  inert?: boolean
  inert_reason?: string
}

/** Un fait qu'on peut écouter, avec les champs de sa charge. */
export type EventChoice = {
  type: string
  label: string
  /** Vide pour un fait du noyau, l'identifiant du plugin sinon. */
  source?: string
  /** Les champs appariables, et donc les variables de gabarit disponibles. */
  fields: string[]
}

/** Un geste qu'on peut poser dans un plan. */
export type GesteChoice = {
  kind: 'action' | 'pipeline'
  ref: string
  label: string
  summary?: string
  /** Vide pour un geste du noyau, l'identifiant du plugin qui le fournit sinon.
   *  Deux gestes peuvent porter le même libellé et ne pas parler à la même forge. */
  source?: string
  fields?: {
    name: string
    label: string
    help?: string
    required?: boolean
    /** Désigne un champ sensible d'un plugin ; n'en porte jamais le contenu. */
    secret?: boolean
    /** Le jeu fermé des valeurs acceptées, quand il l'est. Proposé en suggestions
     *  et non en sélecteur : un gabarit doit pouvoir le traverser. */
    options?: string[]
    placeholder?: string
  }[]
  /** Ce que le pipeline laisse derrière lui — `image`, `results`. Vide pour une
   *  action, qui ne produit rien d'observable. C'est ce qui décide dans quelle
   *  étape à rôle il a le droit d'aller. */
  produces?: string[]
}

export type Catalogue = {
  events: EventChoice[]
  gestes: GesteChoice[]
  when: string[]
  roles: RoleChoice[]
}

export function useReactions(slug: string) {
  return useQuery({
    queryKey: ['reactions', slug],
    queryFn: () =>
      request<{ reactions: Reaction[] }>(`/projects/${encodeURIComponent(slug)}/reactions`),
    enabled: slug !== '',
  })
}

/** Ce que cette version sait faire. Chargé une fois : le registre vit dans le
 *  binaire et ne bouge pas d'une requête à l'autre. */
export function useAutomationCatalogue() {
  return useQuery({
    queryKey: ['automations', 'catalogue'],
    queryFn: () => request<Catalogue>('/admin/automations/catalogue'),
    staleTime: Infinity,
  })
}

export type ReactionDraft = {
  name: string
  event_type: string
  enabled: boolean
  match: Record<string, string>
  plan: Plan
}

export function useSaveReaction(slug: string) {
  const queryClient = useQueryClient()

  return useMutation({
    // Un identifiant : c'est un remplacement. Sans : une création. L'identifiant
    // est dans le chemin et jamais dans le corps — un corps qui le porterait
    // permettrait de déplacer une réaction d'un projet à l'autre.
    mutationFn: ({ id, draft }: { id?: number; draft: ReactionDraft }) =>
      request<{ id: number }>(
        id === undefined
          ? `/projects/${encodeURIComponent(slug)}/reactions`
          : `/projects/${encodeURIComponent(slug)}/reactions/${id}`,
        { method: id === undefined ? 'POST' : 'PUT', body: JSON.stringify(draft) },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['reactions', slug] })
    },
  })
}

export function useDeleteReaction(slug: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (id: number) =>
      request<{ id: number }>(`/projects/${encodeURIComponent(slug)}/reactions/${id}`, {
        method: 'DELETE',
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['reactions', slug] })
    },
  })
}

export function useAutomation(slug: string) {
  return useQuery({
    queryKey: ['automation', slug],
    queryFn: () => request<Automation>(`/projects/${encodeURIComponent(slug)}/automation`),
    enabled: slug !== '',
  })
}

export function useSaveAutomation(slug: string) {
  const queryClient = useQueryClient()

  return useMutation({
    // Toujours un PUT, jamais un POST : la clé est le projet, et il n'y a qu'une
    // réponse à « que fait-on quand une release est déclarée ? ».
    mutationFn: (plan: Plan) =>
      request<Automation>(`/projects/${encodeURIComponent(slug)}/automation`, {
        method: 'PUT',
        body: JSON.stringify({ plan }),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['automation', slug] })
    },
  })
}

/** Les gestes qu'une étape de ce rôle accepte.
 *
 *  Le serveur refuse de toute façon, mais un formulaire qui laisse choisir puis
 *  refuse est un formulaire qu'on apprend à contourner. Sans rôle, tout est
 *  admis : une étape sans rôle ne touche pas à l'état de la release. */
export function gestesPourRole(
  catalogue: Catalogue,
  role: string | undefined,
): GesteChoice[] {
  const attendu = catalogue.roles.find((choice) => choice.role === role)
  if (!attendu) return catalogue.gestes
  return catalogue.gestes.filter(
    (geste) => geste.kind === 'pipeline' && (geste.produces ?? []).includes(attendu.requires),
  )
}

/** Un plan neuf : une étape, aucun geste. La forme minimale qui se valide. */
export function emptyPlan(): Plan {
  return { stages: [{ gestes: [] }] }
}

/** Le libellé d'un geste, tel que le catalogue le nomme. */
export function gesteLabel(catalogue: Catalogue | undefined, geste: Geste): string {
  const known = catalogue?.gestes.find(
    (choice) => choice.kind === geste.kind && choice.ref === geste.ref,
  )
  return known?.label ?? geste.ref
}
