import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { useScrollLock } from '../lib/scrollLock'
import { Icon } from './Icon'

/*
 * Confirmation d'une action sortante.
 *
 * Convention de la doc 08 §8.5 : toute action déclenchant une exécution ou un
 * déploiement rappelle explicitement **l'environnement, la version et l'action**.
 * Un « Confirmer ? » sans rappel ne protège de rien — l'utilisateur confirme ce
 * qu'il croit avoir cliqué, pas ce qui est réellement en jeu.
 *
 * `challenge` ajoute une ressaisie : la production exige de retaper le numéro de
 * version. Le coût de la friction est négligeable devant celui d'une erreur, et
 * ce geste est le seul qui distingue une confirmation lue d'une confirmation
 * réflexe.
 */
export function ConfirmDialog({
  title,
  children,
  confirmLabel,
  challenge,
  onConfirm,
  onCancel,
  pending = false,
}: {
  title: string
  children: ReactNode
  confirmLabel: string
  /** Valeur à ressaisir pour débloquer la confirmation. Absente, pas de ressaisie. */
  challenge?: string
  onConfirm: () => void
  onCancel: () => void
  pending?: boolean
}) {
  const [typed, setTyped] = useState('')
  const dialog = useRef<HTMLDivElement>(null)
  const titleId = useId()
  const challengeId = useId()

  // Le verrou est compté : cette boîte s'ouvre souvent par-dessus une `Modal`,
  // et la refermer ne doit pas rendre le défilement à celle qui reste.
  useScrollLock(true)

  // Échap annule. Le raccourci est attendu d'une boîte modale, et son absence
  // oblige à viser un bouton pour sortir de ce qu'on ne voulait pas ouvrir.
  useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if (event.key === 'Escape') onCancel()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onCancel])

  // Le focus entre dans la boîte : sans cela, la navigation au clavier reste sur
  // la page derrière et le contenu de la confirmation n'est jamais lu.
  useEffect(() => {
    dialog.current?.focus()
  }, [])

  const unlocked = !challenge || typed.trim() === challenge

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-zinc-900/50 p-4">
      <div
        ref={dialog}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        className="w-full max-w-md rounded-xl border border-zinc-200 bg-white p-4 shadow-lg"
      >
        <h2 id={titleId} className="flex items-center gap-2 text-base font-semibold text-zinc-900">
          <Icon name="alert" className="size-4 shrink-0 text-warn-700" />
          {title}
        </h2>

        <div className="mt-3 flex flex-col gap-1 text-sm text-zinc-700">{children}</div>

        {challenge && (
          <div className="mt-3">
            <label htmlFor={challengeId} className="block text-sm text-zinc-700">
              Saisir <span className="font-mono font-medium text-zinc-900">{challenge}</span> pour confirmer
            </label>
            <input
              id={challengeId}
              value={typed}
              onChange={(event) => setTyped(event.target.value)}
              autoComplete="off"
              className="mt-1 w-full rounded-md border border-zinc-300 bg-white px-2 py-1 font-mono text-sm text-zinc-900"
            />
          </div>
        )}

        <div className="mt-4 flex justify-end gap-2">
          <button
            type="button"
            onClick={onCancel}
            className="rounded-md border border-zinc-300 bg-white px-3 py-1 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
          >
            Annuler
          </button>
          <button
            type="button"
            onClick={onConfirm}
            disabled={!unlocked || pending}
            className="rounded-md border border-critical-600 bg-critical-50 px-3 py-1 text-sm font-medium text-critical-700 transition-colors hover:border-critical-700 disabled:opacity-50"
          >
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  )
}
