import { Icon } from '../../components/Icon'
import { Slot } from '../../components/Screen'
import { status } from '../../lib/status'
import type { Release } from './api'

/*
 * Ce qu'on lit à côté du nom d'une release.
 *
 * L'état seul répondait mal à la question posée devant une liste : *puis-je
 * livrer celle-ci ?* « Échec » ne disait pas ce qui avait échoué, et l'état de
 * validation ne dit rien de l'endroit où la release tourne.
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
      {/* Les pastilles que des plugins ajoutent à l'emplacement `badges` de
          l'en-tête ou de la liste : où la release tourne, pour un plugin qui
          déploie. */}
      <Slot name="badges" context={{ release }} />
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
