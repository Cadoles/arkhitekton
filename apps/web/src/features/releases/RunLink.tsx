import { Link } from 'react-router'
import { Icon } from '../../components/Icon'

/*
 * Renvoi vers l'exécution qui a produit un rapport.
 *
 * Un rapport dit *qu'un* test a échoué ; les journaux du run disent *pourquoi*.
 * Sans ce lien, retrouver l'exécution demandait d'ouvrir la file, de repérer le
 * bon job parmi ceux de la release et d'en suivre le nom — pour un geste que
 * l'on fait à chaque échec.
 *
 * Absent quand le nom manque : la projection d'un run est purgée avant le
 * rapport qu'il a produit, et un lien mort vaut moins que pas de lien.
 */
export function RunLink({ runName, label = "Voir l'exécution" }: { runName?: string; label?: string }) {
  if (!runName) return null

  return (
    <Link
      to={`/runs/${encodeURIComponent(runName)}`}
      className="inline-flex items-center gap-1.5 rounded-md border border-zinc-300 bg-white px-2 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
    >
      <Icon name="arrow-right" className="size-3.5" />
      {label}
      <span className="font-mono text-zinc-500">{runName}</span>
    </Link>
  )
}
