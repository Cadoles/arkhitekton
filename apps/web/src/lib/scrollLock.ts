import { useEffect } from 'react'
import { APP_SCROLL_ID } from '../components/AppShell'

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
 * Le verrou porte sur le conteneur de défilement de la coquille, pas sur le
 * document : c'est lui qui défile depuis que l'ascenseur a été descendu d'un
 * cran dans la mise en page. Il réserve sa propre gouttière, donc la figer ne
 * décale rien et aucune compensation de largeur n'est nécessaire.
 *
 * Le repli sur `<html>` couvre les écrans rendus hors coquille — la page de
 * connexion — où une boîte peut aussi s'ouvrir.
 */

let locks = 0
let previousOverflow = ''
let locked: HTMLElement | null = null

function scroller(): HTMLElement {
  return document.getElementById(APP_SCROLL_ID) ?? document.documentElement
}

/** Verrouille le défilement de la page tant que `active` est vrai. */
export function useScrollLock(active: boolean) {
  useEffect(() => {
    if (!active) return

    if (locks === 0) {
      // L'élément verrouillé est retenu : le relâchement doit viser celui qu'on
      // a figé, même si la coquille a été démontée entre-temps.
      locked = scroller()
      previousOverflow = locked.style.overflow
      locked.style.overflow = 'hidden'
    }
    locks += 1

    return () => {
      locks -= 1
      if (locks === 0 && locked) {
        locked.style.overflow = previousOverflow
        locked = null
      }
    }
  }, [active])
}
