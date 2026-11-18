import type { SelectHTMLAttributes } from 'react'

const SIZES = {
  xs: 'px-2 py-0.5 text-xs',
  sm: 'px-2 py-1 text-sm',
  md: 'px-2 py-1.5 text-sm',
} as const

/**
 * Un menu déroulant, au style de ceux des automatisations.
 *
 * Le fond et la couleur du texte sont posés explicitement : un `<select>` qui
 * les hérite affiche, en thème sombre, un texte clair sur la liste blanche du
 * navigateur. La taille se choisit ici plutôt que par des classes ajoutées, que
 * Tailwind départagerait au hasard de leur ordre dans la feuille.
 */
export function Select({
  size = 'sm',
  className = '',
  ...props
}: Omit<SelectHTMLAttributes<HTMLSelectElement>, 'size'> & { size?: keyof typeof SIZES }) {
  return (
    <select
      {...props}
      className={`rounded-md border border-zinc-300 bg-white text-zinc-900 ${SIZES[size]} ${className}`}
    />
  )
}
