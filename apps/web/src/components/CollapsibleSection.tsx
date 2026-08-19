import { useId, useState, type ReactNode } from 'react'
import { Icon } from './Icon'

/*
 * Section repliable — reprise de la maquette, trait pour trait.
 *
 * L'écran d'une release empile beaucoup de blocs : images, déploiement, tests,
 * bench, sécurité, validation, exécutions, exports. Les laisser tous déployés
 * oblige à parcourir la page entière pour atteindre le dernier, alors qu'on ne
 * vient consulter qu'un ou deux d'entre eux à la fois.
 *
 * Le compte figure dans l'en-tête, dans une pastille : c'est ce qui permet de
 * décider s'il vaut la peine d'ouvrir. Une section repliée sans indication de son
 * contenu se replie une fois et ne se rouvre jamais.
 *
 * Le titre est **dans le bouton**, lui-même dans un `h2` : le plan du document
 * reste lisible aux lecteurs d'écran, et la section entière est atteignable au
 * clavier.
 */
export function CollapsibleSection({
  title,
  count,
  defaultOpen = true,
  action,
  children,
}: {
  title: ReactNode
  /** Absent, aucune pastille — toutes les sections ne dénombrent pas. */
  count?: number
  defaultOpen?: boolean
  /** Commande propre à la section, à droite de l'en-tête. */
  action?: ReactNode
  children: ReactNode
}) {
  const [open, setOpen] = useState(defaultOpen)
  const bodyId = useId()

  return (
    <section className="rounded-lg border border-zinc-200 bg-white">
      <div className="flex items-center gap-2 pr-4">
        <h2 className="min-w-0 flex-1">
          <button
            type="button"
            className="flex w-full items-center gap-2 px-4 py-3 text-left"
            aria-expanded={open}
            aria-controls={bodyId}
            onClick={() => setOpen((value) => !value)}
          >
            <Icon
              name="chevron"
              className={`size-4 shrink-0 text-zinc-400 transition-transform ${open ? '' : '-rotate-90'}`}
            />
            <span className="truncate font-semibold text-zinc-900">{title}</span>
            {count !== undefined && (
              <span className="ml-auto rounded-full bg-zinc-100 px-2 py-0.5 font-mono text-xs text-zinc-600">
                {count}
              </span>
            )}
          </button>
        </h2>
        {/* Hors du bouton : une action imbriquée dans un bouton n'est pas un
            élément valide, et cliquer dessus replierait la section. */}
        {action}
      </div>

      {open && (
        <div id={bodyId} className="border-t border-zinc-100 px-4 py-4">
          {children}
        </div>
      )}
    </section>
  )
}
