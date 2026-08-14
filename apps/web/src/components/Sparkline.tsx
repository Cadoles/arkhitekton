/*
 * Tendance d'une mesure sur quelques points.
 *
 * SVG à la main plutôt qu'une bibliothèque de graphiques : six points et une
 * ligne ne justifient pas une dépendance, et le rendu doit suivre les jetons de
 * thème comme le reste de l'interface.
 *
 * Le composant ne décide de rien : il reçoit des points déjà filtrés. C'est le
 * serveur qui écarte les mesures non comparables, parce qu'une courbe mêlant des
 * conditions différentes est une tendance inventée (B16) — et ce jugement ne doit
 * pas dépendre du composant qui la dessine.
 */
export function Sparkline({
  points,
  lowerBetter,
  className = '',
}: {
  points: number[]
  lowerBetter: boolean
  className?: string
}) {
  if (points.length < 2) return null

  const min = Math.min(...points)
  const max = Math.max(...points)
  const span = max - min || 1

  const width = 72
  const height = 20
  const path = points
    .map((value, index) => {
      const x = (index / (points.length - 1)) * width
      // L'axe SVG descend : une valeur haute doit être haute à l'écran.
      const y = height - ((value - min) / span) * height
      return `${index === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')

  // `noUncheckedIndexedAccess` : la longueur est déjà contrôlée plus haut, mais
  // le compilateur ne le sait pas, et le lui faire savoir vaut mieux qu'un `!`.
  const first = points.at(0) ?? 0
  const last = points.at(-1) ?? 0
  const improving = lowerBetter ? last <= first : last >= first
  const stroke = improving ? 'stroke-healthy-500' : 'stroke-warn-500'

  return (
    <svg
      viewBox={`0 0 ${width} ${height}`}
      width={width}
      height={height}
      className={`overflow-visible ${className}`}
      role="img"
      aria-label={`Tendance sur ${points.length} mesures, ${improving ? 'stable ou en amélioration' : 'en dégradation'}`}
    >
      <path d={path} fill="none" strokeWidth="1.5" className={stroke} strokeLinecap="round" strokeLinejoin="round" />
      <circle
        cx={width}
        cy={height - ((last - min) / span) * height}
        r="2"
        className={improving ? 'fill-healthy-500' : 'fill-warn-500'}
      />
    </svg>
  )
}
