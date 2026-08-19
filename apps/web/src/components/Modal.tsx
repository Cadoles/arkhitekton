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
export function Modal({
  title,
  children,
  onClose,
  closable = true,
  wide = false,
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
  wide?: boolean
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
      className="fixed inset-0 z-40 flex items-start justify-center overflow-y-auto bg-zinc-900/50 p-4 sm:items-center"
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
        className={`w-full rounded-xl border border-zinc-200 bg-white p-4 shadow-lg ${
          wide ? 'max-w-2xl' : 'max-w-md'
        }`}
      >
        <div className="flex items-start justify-between gap-3">
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

        <div className="mt-3">{children}</div>
      </div>
    </div>
  )
}
