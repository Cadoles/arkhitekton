import { useParams } from 'react-router'
import { Card, EmptyState } from './primitives'

/*
 * Écran non encore implémenté.
 *
 * Le lot 0 livre une application déployée et authentifiée, pas des écrans. Poser
 * les routes dès maintenant sert à deux choses : vérifier le routage et la
 * coquille en conditions réelles, et rendre visible ce qui reste à faire — un
 * lien de navigation qui mène à une page blanche se lit comme une panne.
 *
 * Chaque écran annonce son contenu prévu, repris de la doc 08.
 */

type PlaceholderProps = {
  title: string
  summary: string
  /** Noms des paramètres d'URL à afficher, pour vérifier le routage. */
  params?: string[]
  lot: string
}

export function Placeholder({ title, summary, params = [], lot }: PlaceholderProps) {
  const routeParams = useParams()

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h1 className="text-2xl font-semibold text-zinc-900">{title}</h1>
        <span className="rounded-md border border-zinc-200 bg-white px-2 py-0.5 font-mono text-xs text-zinc-500">
          {lot}
        </span>
      </div>

      <Card>
        <p className="text-sm text-zinc-600">{summary}</p>

        {params.length > 0 && (
          <dl className="mt-4 flex flex-wrap gap-x-6 gap-y-2 border-t border-zinc-200 pt-4">
            {params.map((name) => (
              <div key={name}>
                <dt className="text-xs text-zinc-500">{name}</dt>
                <dd className="font-mono text-sm text-zinc-900">{routeParams[name] ?? '—'}</dd>
              </div>
            ))}
          </dl>
        )}
      </Card>

      <EmptyState>Cet écran n'est pas encore implémenté.</EmptyState>
    </div>
  )
}
