import { useEffect } from 'react'

/*
 * Gel du défilement de la page pendant qu'une surface modale est ouverte.
 *
 * Sans lui, la molette traverse la boîte : le fond défile derrière, et sur une
 * modale plus courte que la page on voit le contenu glisser sous une boîte qui,
 * elle, ne bouge pas. Pire, refermer la boîte laisse le lecteur ailleurs qu'où
 * il était.
 *
 * Le verrou est **compté**, et c'est ce qui le rend correct : `ConfirmDialog`
 * s'ouvre par-dessus `Modal` — créer une release depuis une modale en est le cas
 * courant. Deux composants posant chacun `overflow: hidden` et le retirant à leur
 * démontage, le premier refermé rendrait le défilement alors que le second est
 * toujours là. Le compteur ne relâche qu'à la dernière fermeture.
 *
 * La valeur d'origine est relue au premier verrou plutôt que supposée `visible` :
 * la restaurer en dur écraserait un style posé ailleurs, et ce genre de dégât ne
 * se voit qu'une fois la boîte refermée.
 *
 * Le verrou est posé sur `<html>` et non sur `<body>`, alors que la seconde
 * forme est la plus répandue. C'est `<html>` qui porte `scrollbar-gutter: stable`
 * (voir `styles/theme.css`) : la gouttière reste réservée sous `overflow: hidden`,
 * et masquer la barre ne décale donc rien. Verrouiller `<body>` ferait retomber
 * le viewport sur l'`overflow` de `<html>` et rouvrirait le décalage latéral que
 * cette règle avait précisément fermé.
 *
 * Aucune compensation de largeur n'est donc nécessaire.
 */

let locks = 0
let previousOverflow = ''

/** Verrouille le défilement du document tant que `active` est vrai. */
export function useScrollLock(active: boolean) {
  useEffect(() => {
    if (!active) return

    const root = document.documentElement
    if (locks === 0) {
      previousOverflow = root.style.overflow
      root.style.overflow = 'hidden'
    }
    locks += 1

    return () => {
      locks -= 1
      if (locks === 0) root.style.overflow = previousOverflow
    }
  }, [active])
}
