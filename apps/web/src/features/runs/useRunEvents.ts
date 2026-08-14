import { useEffect } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { streamURL } from '../../lib/api'
import type { Run } from './api'

/*
 * Abonnement au flux d'état des exécutions.
 *
 * Le flux met à jour le cache de TanStack Query plutôt que de tenir un état
 * parallèle. Conséquence : les composants qui lisent la liste n'ont rien à savoir
 * du flux, et la page reste juste si le flux tombe — la requête périodique reprend
 * la main.
 */
export function useRunEvents(enabled = true) {
  const queryClient = useQueryClient()

  useEffect(() => {
    if (!enabled) return

    // EventSource porte le cookie de session sur la même origine, et reconnecte
    // seul en envoyant `Last-Event-ID`. C'est pour cela que le serveur numérote
    // ses événements.
    const source = new EventSource(streamURL('/runs/events'))

    source.addEventListener('run', (event) => {
      let run: Run
      try {
        run = JSON.parse((event as MessageEvent<string>).data) as Run
      } catch {
        return
      }

      // Toutes les listes en cache sont mises à jour, quelle que soit leur
      // limite : la clé partielle `['runs']` les couvre toutes.
      queryClient.setQueriesData<{ runs: Run[] }>({ queryKey: ['runs'] }, (previous) => {
        if (!previous) return previous

        const index = previous.runs.findIndex((existing) => existing.id === run.id)
        if (index === -1) {
          // Un run inconnu vient d'apparaître : il est le plus récent, donc en
          // tête. L'insérer évite d'attendre le prochain rafraîchissement.
          return { runs: [run, ...previous.runs] }
        }

        const runs = [...previous.runs]
        runs[index] = run
        return { runs }
      })

      queryClient.invalidateQueries({ queryKey: ['run', run.name] })
    })

    // Aucun traitement d'erreur : EventSource reconnecte de lui-même, et la
    // requête périodique couvre l'intervalle. Fermer sur erreur supprimerait la
    // reconnexion automatique.
    return () => source.close()
  }, [queryClient, enabled])
}
