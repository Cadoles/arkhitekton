import { Icon } from '../../components/Icon'
import { status, type Health } from '../../lib/status'

/*
 * Jauge de santé — l'élément signature de la maquette.
 *
 * Un anneau de progression, le score au centre, le verdict dessous. La géométrie
 * est celle de la maquette : rayon 52 sur une grille 120, trait de 12, quart de
 * tour à gauche pour que le remplissage parte du haut.
 *
 * Le verdict est **aussi** écrit en toutes lettres sous l'anneau. La couleur seule
 * exclut les daltoniens et disparaît à l'impression (doc 08 §8.5) — c'est la même
 * règle que pour les badges de statut, et elle vaut d'autant plus ici que le
 * chiffre central ne dit pas à lui seul s'il est bon.
 */

const RADIUS = 52
const CIRCUMFERENCE = 2 * Math.PI * RADIUS

const STROKE: Record<Health, string> = {
  healthy: 'text-healthy-500',
  warn: 'text-warn-500',
  critical: 'text-critical-500',
}

export function HealthGauge({
  score,
  health,
  pending = false,
}: {
  score: number
  health: Health | 'na'
  /** Vrai pendant une construction : le score affiché ne décrit pas ce qui se
   *  construit, et l'anneau le dit en tournant plutôt qu'en restant figé. */
  pending?: boolean
}) {
  // Un score non évalué n'est pas un score de zéro : l'anneau reste vide et le
  // centre porte un tiret. Afficher 0 serait le jugement le plus sévère possible
  // sur une release qu'on n'a pas regardée (B11).
  const measured = health !== 'na'
  const bounded = Math.min(100, Math.max(0, score))
  const offset = measured ? CIRCUMFERENCE * (1 - bounded / 100) : CIRCUMFERENCE
  const style = measured ? status.health(health) : undefined

  return (
    <div className="flex flex-col items-center gap-2">
      <div
        className="relative"
        role="img"
        aria-label={
          measured
            ? `Score de qualité ${score} sur 100 — ${style?.label}`
            : 'Score de qualité non évalué'
        }
      >
        <svg
          viewBox="0 0 120 120"
          className={`size-32 -rotate-90 ${pending ? 'animate-spin [animation-duration:3s]' : ''}`}
        >
          <circle
            cx="60"
            cy="60"
            r={RADIUS}
            fill="none"
            stroke="currentColor"
            strokeWidth="12"
            className="text-zinc-100"
          />
          <circle
            cx="60"
            cy="60"
            r={RADIUS}
            fill="none"
            stroke="currentColor"
            strokeWidth="12"
            strokeLinecap="round"
            strokeDasharray={CIRCUMFERENCE}
            strokeDashoffset={offset}
            className={`transition-[stroke-dashoffset] duration-500 ${
              measured ? STROKE[health] : 'text-zinc-200'
            }`}
          />
        </svg>
        {/* Le centre ne tourne pas avec l'anneau : un chiffre en rotation est
            illisible, et c'est justement ce qu'on vient lire. */}
        <div className="absolute inset-0 flex flex-col items-center justify-center">
          <span className="font-mono text-3xl font-semibold text-zinc-900">
            {measured ? score : '—'}
          </span>
          <span className="text-xs text-zinc-400">/ 100</span>
        </div>
      </div>

      {pending ? (
        <span className="text-sm text-zinc-500">Recalcul en attente</span>
      ) : style ? (
        <span className={`inline-flex items-center gap-1 text-sm font-medium ${style.text}`}>
          <Icon name={style.icon} className="size-4" />
          {style.label}
        </span>
      ) : (
        <span className="text-sm text-zinc-500">Non évalué</span>
      )}
    </div>
  )
}
