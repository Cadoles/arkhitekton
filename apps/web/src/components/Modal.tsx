import { useEffect, useId, useRef, type ReactNode } from 'react'
import { Icon } from './Icon'

/*
 * Boîte modale générique.
 *
 * Distincte de `ConfirmDialog`, qui n'est pas une modale générique mais le rappel
 * d'une action sortante — environnement, version, action — avec sa ressaisie. Les
 * fusionner ferait de la confirmation un cas particulier d'affichage, alors que
 * c'est une convention de sécurité (doc 08 §8.5).
 *
 * Les mêmes règles d'accessibilité s'appliquent aux deux : Échap ferme, le focus
 * entre dans la boîte, et le fond est cliquable pour sortir de ce qu'on n'avait
 * pas l'intention d'ouvrir.
 */
const WIDTH = {
  sm: 'max-w-md',
  md: 'max-w-2xl',
  // Au-delà de la largeur du contenu de page — `max-w-6xl` — la boîte cesserait
  // de se lire comme une surface posée par-dessus.
  lg: 'max-w-6xl',
} as const

export function Modal({
  title,
  children,
  onClose,
  closable = true,
  size = 'md',
}: {
  title: string
  children: ReactNode
  onClose: () => void
  /**
   * Faux quand une boîte s'ouvre par-dessus.
   *
   * Sans cela, Échap ferme les deux d'un coup : les deux composants écoutent le
   * même événement sur `document`, et l'ordre d'enregistrement fait que la modale
   * de fond réagit la première. L'utilisateur annulerait une confirmation et
   * perdrait sa saisie du même geste.
   */
  closable?: boolean
  /**
   * Largeur maximale.
   *
   * Trois tailles et non un booléen : un formulaire de création et une liste de
   * deux cents constats n'ont pas la même largeur utile. Étaler un formulaire de
   * six champs sur toute la page éloigne les libellés de leurs champs ; borner
   * une table de résultats à la largeur d'un formulaire la fait défiler
   * latéralement pour rien.
   */
  size?: 'sm' | 'md' | 'lg'
}) {
  const dialog = useRef<HTMLDivElement>(null)
  const titleId = useId()

  useEffect(() => {
    if (!closable) return
    function onKey(event: KeyboardEvent) {
      if (event.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [closable, onClose])

  useEffect(() => {
    dialog.current?.focus()
  }, [])

  return (
    <div
      // Le fond ne défile pas : c'est la boîte qui se borne à la hauteur visible
      // et fait défiler son propre corps. Un fond défilant centré verticalement
      // ampute son contenu **des deux côtés** — le centrage place le trop-plein
      // au-dessus du point de départ du défilement, hors d'atteinte. Constaté sur
      // l'audit de sécurité, dont la liste de constats dépasse l'écran.
      className="fixed inset-0 z-40 flex items-center justify-center bg-zinc-900/50 p-4"
      // Le fond ferme, le contenu non : `onClick` sur le conteneur se déclenche
      // aussi pour les clics remontés depuis l'intérieur, d'où le test de cible.
      onClick={(event) => {
        if (closable && event.target === event.currentTarget) onClose()
      }}
    >
      <div
        ref={dialog}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        className={`flex max-h-full w-full flex-col rounded-xl border border-zinc-200 bg-white shadow-lg ${WIDTH[size]}`}
      >
        {/* L'en-tête reste en place pendant que le corps défile : sur une liste
            de deux cents constats, le titre et la fermeture disparaîtraient dès
            le premier coup de molette. */}
        <div className="flex shrink-0 items-start justify-between gap-3 border-b border-zinc-200 p-4">
          <h2 id={titleId} className="text-base font-semibold text-zinc-900">
            {title}
          </h2>
          <button
            type="button"
            onClick={onClose}
            aria-label="Fermer"
            className="-m-1 rounded-md p-1 text-zinc-500 transition-colors hover:bg-zinc-100 hover:text-zinc-900"
          >
            <Icon name="cross" className="size-4" />
          </button>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto p-4">{children}</div>
      </div>
    </div>
  )
}
