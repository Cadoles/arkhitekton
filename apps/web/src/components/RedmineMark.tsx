import redmineLogo from '../assets/redmine_logo.svg'

/*
 * Marque de Redmine.
 *
 * À part du jeu d'icônes : celui-ci est dessiné au trait sur une grille 24×24 et
 * hérite de la couleur du texte, tandis qu'une marque de produit est pleine et
 * porte ses propres couleurs. La mêler aux autres l'aurait forcée dans une
 * grammaire qui n'est pas la sienne, et aurait fait passer pour une icône du
 * dashboard ce qui désigne un logiciel tiers.
 *
 * Le tracé n'est plus ici : il vient du fichier officiel, réduit à l'arche. Deux
 * versions l'ont précédé — un losange rouge posé faute d'avoir la marque, puis
 * une arche redessinée à l'œil — et la seconde était la plus trompeuse : elle
 * ressemblait assez pour qu'on ne la vérifie pas, avec neuf claveaux au lieu de
 * sept.
 *
 * Une balise `img` plutôt qu'un SVG inline, et sans dépendance pour transformer
 * le fichier en composant : il reste la source, remplaçable sans toucher au
 * code. Vite l'incorpore en data-URI — il pèse moins que sa limite d'inclusion —
 * donc l'affichage ne coûte pas de requête supplémentaire.
 *
 * Les proportions sont celles de la marque, plus large que haute. Les appelants
 * lui donnent donc une boîte de ce rapport : sous une classe carrée, elle
 * s'afficherait à l'échelle de sa hauteur et paraîtrait deux fois trop petite à
 * côté du texte.
 */
export function RedmineMark({ className }: { className?: string }) {
  return (
    // Décorative : ses deux emplois sont accompagnés du mot « Redmine », et
    // l'annoncer le ferait lire deux fois.
    <img src={redmineLogo} alt="" className={className} />
  )
}
