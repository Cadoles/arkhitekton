import { Icon } from '../../components/Icon'
import { status } from '../../lib/status'
import { useEnvironments } from '../environments/api'
import type { Release } from './api'

/*
 * Ce qu'on lit à côté du nom d'une release.
 *
 * L'état seul — « Livrée », « Échec » — répondait mal à la question posée devant
 * une liste : *puis-je livrer celle-ci ?* « Échec » ne disait pas ce qui avait
 * échoué, et « Livrée » ne disait pas où elle tourne encore.
 *
 * Trois pastilles y répondent, dans l'ordre où l'on se pose les questions :
 *
 *   1. l'image existe-t-elle ? Une pastille **seulement** si la dernière
 *      construction a échoué — le succès est le cas courant et n'a pas besoin
 *      d'être annoncé ;
 *   2. que valent ses tests ? Le score, au code couleur du verdict ;
 *   3. tourne-t-elle quelque part ? Les environnements, et rien si une autre
 *      release y a été déployée depuis.
 *
 * L'état de la release reste affiché à côté : il dit où elle en est dans son
 * cycle, ce qu'aucune des trois ne recouvre.
 */
export function ReleaseBadges({ release }: { release: Release }) {
  return (
    <>
      {release.build_failed && (
        <span className="inline-flex items-center gap-1 rounded-md border border-critical-500 bg-critical-50 px-2 py-0.5 text-xs font-medium text-critical-700">
          <Icon name="cross" className="size-3.5" />
          Construction en échec
        </span>
      )}

      <ScoreBadge score={release.score} />
      <DeployedBadges environments={release.deployed_on ?? []} />
    </>
  )
}

/**
 * Le score, au code couleur de son verdict.
 *
 * Absent tant que rien n'a été mesuré : afficher « 0 » sur une release dont
 * aucun test n'a tourné serait le jugement le plus sévère possible, et le plus
 * faux (B11).
 */
function ScoreBadge({ score }: { score?: Release['score'] }) {
  if (!score?.measured || score.health === 'na') return null

  const style = status.health(score.health)

  return (
    <span
      title={`Score de qualité — ${style.label}`}
      className={`inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-xs font-medium ${style.bg} ${style.border} ${style.text}`}
    >
      <Icon name={style.icon} className="size-3.5" />
      <span className="font-mono">{score.value}</span>
      <span className="opacity-70">/ 100</span>
    </span>
  )
}

/**
 * Les environnements où la release tourne.
 *
 * Le libellé vient du catalogue : `prod` seul se lit mal dans une liste, et
 * l'écran des environnements affiche « Production ». Le repli sur l'identifiant
 * couvre un environnement retiré du catalogue mais encore observé.
 */
function DeployedBadges({ environments }: { environments: string[] }) {
  const known = useEnvironments()

  if (environments.length === 0) return null

  const label = (id: string) =>
    known.data?.environments.find((entry) => entry.id === id)?.label ?? id

  return (
    <>
      {environments.map((id) => (
        <span
          key={id}
          title={`Déployée sur ${label(id)}`}
          className="inline-flex items-center gap-1 rounded-md border border-zinc-300 bg-zinc-100 px-2 py-0.5 text-xs font-medium text-zinc-700"
        >
          <Icon name="layers" className="size-3.5" />
          {label(id)}
        </span>
      ))}
    </>
  )
}
