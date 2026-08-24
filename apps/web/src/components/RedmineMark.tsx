/*
 * Marque de Redmine : l'arche à neuf claveaux.
 *
 * À part du jeu d'icônes : celui-ci est dessiné au trait sur une grille 24×24 et
 * hérite de la couleur du texte, tandis qu'une marque de produit est pleine et
 * porte ses propres couleurs. La mêler aux autres l'aurait forcée dans une
 * grammaire qui n'est pas la sienne, et aurait fait passer pour une icône du
 * dashboard ce qui désigne un logiciel tiers.
 *
 * Le tracé remplace un losange rouge qui tenait lieu de repère faute d'avoir la
 * marque sous la main. Il en reprend la construction : neuf claveaux sur un
 * demi-cercle, arête extérieure droite — c'est ce qui donne à l'arche son aspect
 * facetté plutôt que celui d'un éventail — et arête intérieure courbe. Les teintes
 * vont du plus sombre aux naissances au plus clair à la clef.
 *
 * Les proportions sont celles de la marque, plus large que haute. Les appelants
 * lui donnent donc une boîte de ce rapport : sous une classe carrée, elle
 * s'afficherait à l'échelle de sa hauteur et paraîtrait deux fois trop petite à
 * côté du texte.
 */
export function RedmineMark({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 751 511"
      className={className}
      // Décorative, comme les icônes : ses deux emplois sont accompagnés du mot
      // « Redmine », et l'annoncer le ferait lire deux fois.
      aria-hidden="true"
      focusable="false"
    >
      <path d="M0.6 501.8L20.1 391.4L172.7 442.7A214 214 0 0 0 161.6 505.8Z" fill="#A52420" />
      <path d="M26.4 374.2L82.4 277.0L208.3 377.5A214 214 0 0 0 176.3 432.9Z" fill="#C12A27" />
      <path d="M94.2 263.0L180.1 190.9L264.0 328.3A214 214 0 0 0 215.0 369.5Z" fill="#D2322E" />
      <path d="M196.0 181.8L301.4 143.4L333.2 301.2A214 214 0 0 0 273.1 323.1Z" fill="#DE3A35" />
      <path d="M319.4 140.2L431.6 140.2L407.5 299.4A214 214 0 0 0 343.5 299.4Z" fill="#E8433E" />
      <path d="M449.6 143.4L555.0 181.8L477.9 323.1A214 214 0 0 0 417.8 301.2Z" fill="#DE3A35" />
      <path d="M570.9 190.9L656.8 263.0L536.0 369.5A214 214 0 0 0 487.0 328.3Z" fill="#D2322E" />
      <path d="M668.6 277.0L724.6 374.2L574.7 432.9A214 214 0 0 0 542.7 377.5Z" fill="#C12A27" />
      <path d="M730.9 391.4L750.4 501.8L589.4 505.8A214 214 0 0 0 578.3 442.7Z" fill="#A52420" />
    </svg>
  )
}
