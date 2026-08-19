/*
 * Marque de Redmine.
 *
 * À part du jeu d'icônes : celui-ci est dessiné au trait sur une grille 24×24 et
 * hérite de la couleur du texte, tandis qu'une marque de produit est pleine et
 * porte sa propre couleur. La mêler aux autres l'aurait forcée dans une grammaire
 * qui n'est pas la sienne, et aurait fait passer pour une icône du dashboard ce
 * qui désigne un logiciel tiers.
 *
 * **Ce n'est pas l'asset officiel.** C'est une évocation — le losange rouge de
 * Redmine — dessinée ici faute d'avoir le fichier source. Elle remplit son rôle
 * de repère visuel, mais si l'exactitude compte, remplacer le contenu de ce
 * composant par le SVG officiel suffit : rien d'autre n'a à changer.
 */
export function RedmineMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={className} aria-hidden="true" focusable="false">
      <path d="M12 2 22 12 12 22 2 12z" fill="#B32024" />
      <path d="M12 7.5 16.5 12 12 16.5 7.5 12z" fill="#fff" fillOpacity=".85" />
    </svg>
  )
}
