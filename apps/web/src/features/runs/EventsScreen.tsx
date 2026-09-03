import { Card, EmptyState, Skeleton } from '../../components/primitives'
import { formatDateTime } from '../../lib/format'
import { EVENT_LABEL, outcomeOf, useProjectEvents, type ProjectEvent } from './events'

/*
 * Le fil des faits d'un projet.
 *
 * Personne n'y réagit encore, et c'est assumé : cette vue existe pour rendre
 * répondable la question qui viendra ensuite — « pourquoi rien ne s'est
 * passé ? » — dont les deux réponses ne se corrigent pas pareil. Ou bien le fait
 * n'a pas été émis, ou bien aucune réaction ne l'appariait. Sans le fil, les deux
 * sont indiscernables.
 *
 * Sous la coquille des exécutions, à côté des runs et de la file : ce sont trois
 * façons de regarder ce que le dashboard a fait, et les séparer obligerait à
 * naviguer pour rapprocher un fait du run qui l'a produit.
 */
export function EventsScreen() {
  const { data, isPending, error } = useProjectEvents()

  return (
    <Card title="Faits constatés">
      {isPending && <Skeleton className="h-32 w-full" />}

      {error && (
        <EmptyState>Les faits n'ont pas pu être lus : {(error as Error).message}</EmptyState>
      )}

      {data &&
        (data.events.length === 0 ? (
          <EmptyState>
            Aucun fait constaté pour ce projet. Ils apparaissent au fil des créations de
            release, des constructions et des exécutions.
          </EmptyState>
        ) : (
          <ul className="flex flex-col divide-y divide-zinc-200">
            {data.events.map((event) => (
              <EventRow key={event.id} event={event} />
            ))}
          </ul>
        ))}
    </Card>
  )
}

function EventRow({ event }: { event: ProjectEvent }) {
  const outcome = outcomeOf(event)

  return (
    <li className="flex flex-wrap items-baseline gap-x-3 gap-y-1 py-2">
      <span className="text-sm font-medium text-zinc-900">
        {/* Un type inconnu s'affiche tel quel plutôt que de disparaître : les
            faits d'un plugin arriveront avec leurs propres libellés. */}
        {EVENT_LABEL[event.type] ?? event.type}
      </span>

      {event.subject && (
        <span className="font-mono text-xs text-zinc-700">{event.subject}</span>
      )}

      {outcome && <OutcomeBadge outcome={outcome} />}

      {/* La source n'apparaît que pour un plugin : l'afficher sur chaque ligne du
          noyau serait une colonne vide répétée. */}
      {event.source && (
        <span className="rounded border border-zinc-300 px-1.5 py-0.5 text-xs text-zinc-600">
          {event.source}
        </span>
      )}

      <span className="ml-auto text-xs text-zinc-500" title={event.type}>
        {formatDateTime(event.occurred_at)}
      </span>

      {/* Rendu seulement quand les deux instants diffèrent — donc quand un fait
          ancien vient d'être découvert. Le cas n'existe pas encore ; il existera
          avec les sondes, et c'est là qu'il méritera d'être vu. */}
      {event.recorded_at && (
        <span className="w-full text-xs text-zinc-400">
          constaté le {formatDateTime(event.recorded_at)}
        </span>
      )}
    </li>
  )
}

/** L'issue d'un fait, quand la question se pose. */
function OutcomeBadge({ outcome }: { outcome: string }) {
  const tone =
    outcome === 'succeeded'
      ? 'border-healthy-500 bg-healthy-50 text-healthy-700'
      : outcome === 'cancelled'
        ? 'border-zinc-300 bg-zinc-50 text-zinc-600'
        : 'border-critical-500 bg-critical-50 text-critical-700'

  const label =
    outcome === 'succeeded' ? 'réussi' : outcome === 'cancelled' ? 'annulé' : 'échoué'

  return <span className={`rounded border px-1.5 py-0.5 text-xs ${tone}`}>{label}</span>
}
