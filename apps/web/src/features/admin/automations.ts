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

/** Une étape : des gestes exécutés en parallèle. */
export type Stage = { label?: string; gestes: Geste[] }

export type Plan = { stages: Stage[] }

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
}

export type Catalogue = {
  events: EventChoice[]
  gestes: GesteChoice[]
  when: string[]
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
