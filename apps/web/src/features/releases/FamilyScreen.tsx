import { Link, useParams } from 'react-router'
import { Icon } from '../../components/Icon'
import { FamilyDetail } from './FamilyDetail'
import { FAMILY_LABEL } from './tests'

/*
 * Détail d'une famille de tests, en page.
 *
 * Le contenu est celui de la modale ouverte depuis une tuile du score — même
 * composant, pas une copie. Cette page reste : elle est adressable, donc
 * partageable dans un ticket, et une modale ne l'est pas.
 */
export function FamilyScreen() {
  const { tag, family } = useParams()
  const label = FAMILY_LABEL[family ?? ''] ?? family

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <Link
          to={`/releases/${encodeURIComponent(tag ?? '')}`}
          className="flex items-center gap-1.5 rounded-md border border-zinc-300 bg-white px-2.5 py-1.5 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
        >
          <Icon name="arrow-left" className="size-4" />
          <span className="font-mono">{tag}</span>
        </Link>
        <h1 className="text-2xl font-semibold text-zinc-900">{label}</h1>
      </div>

      <section className="rounded-lg border border-zinc-200 bg-white p-4">
        <FamilyDetail tag={tag} family={family} />
      </section>
    </div>
  )
}
