/*
 * Primitives de mise en page, reprises de la maquette.
 *
 * Regroupées dans un fichier parce qu'elles sont indissociables : la carte, le
 * badge de statut, l'état vide et le squelette forment le vocabulaire commun de
 * tous les écrans. Les séparer donnerait quatre fichiers de dix lignes qu'on
 * importerait toujours ensemble.
 */

import type { ReactNode } from 'react'
import { Icon } from './Icon'
import type { StatusStyle } from '../lib/status'

export function Card({
  title,
  action,
  children,
  className = '',
}: {
  title?: ReactNode
  action?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <section className={`rounded-xl border border-zinc-200 bg-white p-4 ${className}`}>
      {(title || action) && (
        <header className="mb-3 flex items-center justify-between gap-2">
          {title && <h2 className="text-sm font-semibold text-zinc-900">{title}</h2>}
          {action}
        </header>
      )}
      {children}
    </section>
  )
}

/**
 * Badge de statut : couleur **et** icône **et** libellé.
 *
 * Les trois ensemble, toujours. La couleur seule exclut les daltoniens et
 * disparaît à l'impression d'un bon de livraison ; l'icône seule est ambiguë.
 */
export function StatusBadge({ style, className = '' }: { style: StatusStyle; className?: string }) {
  return (
    <span
      className={`inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs font-medium ${style.bg} ${style.border} ${style.text} ${className}`}
    >
      <Icon name={style.icon} className="size-3.5" />
      {style.label}
    </span>
  )
}

/**
 * État vide.
 *
 * Toujours formulé en français et, quand elle existe, avec l'action possible :
 * « aucune donnée » sans suite laisse l'utilisateur devant un mur (doc 08 §8.5).
 */
export function EmptyState({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed border-zinc-300 px-4 py-10 text-center">
      <p className="text-sm text-zinc-500">{children}</p>
      {action}
    </div>
  )
}

/**
 * Squelette de chargement, aux dimensions du contenu attendu.
 *
 * Un spinner ne dit pas ce qui arrive et laisse la page sauter quand le contenu
 * se pose. Chaque bloc charge indépendamment (doc 08 §8.5).
 */
export function Skeleton({ className = '' }: { className?: string }) {
  return <div className={`animate-pulse rounded bg-zinc-200 ${className}`} aria-hidden="true" />
}

/** Bloc d'erreur. Le message vient du serveur, l'action de réessai de l'appelant. */
export function ErrorState({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="flex flex-col items-start gap-3 rounded-lg border border-critical-500 bg-critical-50 p-4">
      <p className="flex items-center gap-2 text-sm text-critical-700">
        <Icon name="alert" className="size-4 shrink-0" />
        {message}
      </p>
      {onRetry && (
        <button
          type="button"
          onClick={onRetry}
          className="rounded-md border border-zinc-300 bg-white px-3 py-1 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
        >
          Réessayer
        </button>
      )}
    </div>
  )
}
