import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useNavigate } from 'react-router'
import { useVirtualizer } from '@tanstack/react-virtual'
import { Icon } from '../../components/Icon'
import { ErrorState, Skeleton } from '../../components/primitives'
import { useTaskLog } from './useTaskLog'

/*
 * Visionneuse de journal.
 *
 * Le composant le plus exigeant de l'interface : un pipeline d'intégration produit
 * des dizaines de milliers de lignes, et les rendre toutes fige l'onglet. Le rendu
 * est donc virtualisé — seules les lignes visibles existent dans le DOM.
 *
 * Trois comportements sont attendus d'une visionneuse de journal, et leur
 * interaction est la vraie difficulté :
 *
 *   - **le suivi**, qui colle la vue au bas du flux ;
 *   - **la remontée**, qui doit interrompre le suivi — sinon la vue redescend
 *     sous les doigts de l'utilisateur au premier événement reçu ;
 *   - **l'ancrage** sur une ligne, partageable par URL, qui doit aussi
 *     l'interrompre.
 */

const LINE_HEIGHT = 20

export function LogViewer({
  runName,
  taskName,
  sealed,
}: {
  runName: string
  taskName: string
  sealed: boolean
}) {
  const { lines, loading, error, complete, loadEarlier } = useTaskLog(runName, taskName, sealed)

  const containerRef = useRef<HTMLDivElement>(null)
  const [following, setFollowing] = useState(!sealed)

  // L'ancre vient du routeur et non de `window.location`. La différence n'est pas
  // théorique : le composant est monté après la résolution de la requête, et lire
  // l'objet global à ce moment-là ne donnait rien — le routeur est la seule source
  // qui reste juste quelle que soit la séquence de rendu.
  // L'ancre a une source unique : l'URL. Un état local en parallèle finirait par
  // la contredire — un clic ancien resterait prioritaire sur un lien fraîchement
  // ouvert, et rien dans l'interface ne l'expliquerait.
  const { hash } = useLocation()
  const navigate = useNavigate()
  const anchor = useMemo(() => parseAnchor(hash), [hash])
  // Marque le premier positionnement. Sans lui, la vue s'ouvrirait au début du
  // contenu chargé — or ce contenu **est** la fin du journal, et c'est la
  // dernière ligne qu'on vient lire : l'échec est à la fin.
  const positioned = useRef(false)

  // La règle react-hooks signale que `useVirtualizer` rend des fonctions non
  // mémoïsables. C'est exact et sans conséquence ici : le virtualiseur est un
  // système externe qui gère lui-même son cycle de vie, et il n'existe pas
  // d'alternative en dépendance nulle qui tienne des dizaines de milliers de
  // lignes. L'exception est donc assumée et bornée à cet appel.
  // eslint-disable-next-line react-hooks/incompatible-library
  const virtualizer = useVirtualizer({
    count: lines.length,
    getScrollElement: () => containerRef.current,
    // Une **estimation** seulement : la hauteur réelle est mesurée après rendu.
    // Elle l'était autrefois imposée, et c'était le défaut : une ligne trop
    // longue pour la largeur se replie sur plusieurs hauteurs de texte, mais la
    // suivante restait positionnée à une hauteur de ligne de distance — les
    // lignes se recouvraient. `measureElement`, posé en référence sur chaque
    // ligne rendue, corrige la position des suivantes.
    estimateSize: () => LINE_HEIGHT,
    // Quelques lignes au-delà du viewport : sans marge, un défilement rapide
    // laisse apparaître des blancs le temps du rendu.
    overscan: 20,
  })

  // `useVirtualizer` rend un objet d'identité nouvelle à chaque rendu. Le placer
  // dans les dépendances d'un effet le ferait donc rejouer en permanence, et le
  // positionnement se battrait contre lui-même — c'est ce que signale la règle
  // désactivée plus haut. La référence coupe court.
  const virtualizerRef = useRef(virtualizer)
  virtualizerRef.current = virtualizer

  // Un seul effet décide de la position, et l'ordre de ses branches est la règle
  // métier : une ancre explicite prime sur le suivi, sinon le journal s'ouvre
  // sur sa fin. Répartir cette décision sur plusieurs effets les faisait se
  // contredire, le dernier exécuté l'emportant.
  useEffect(() => {
    if (lines.length === 0) return

    if (anchor !== null && !positioned.current) {
      positioned.current = true
      setFollowing(false)
      virtualizerRef.current.scrollToIndex(Math.min(anchor, lines.length - 1), { align: 'center' })
      return
    }

    if (!positioned.current) {
      // Premier affichage sans ancre : la fin. Le contenu chargé **est** la fin
      // du journal, et c'est la dernière ligne qu'on vient lire.
      positioned.current = true
      virtualizerRef.current.scrollToIndex(lines.length - 1, { align: 'end' })
      return
    }

    if (following) {
      virtualizerRef.current.scrollToIndex(lines.length - 1, { align: 'end' })
    }
  }, [anchor, following, lines.length])

  const onScroll = useCallback(() => {
    const element = containerRef.current
    if (!element) return

    // Une tolérance est indispensable : les hauteurs estimées par le
    // virtualiseur font que le bas exact n'est presque jamais atteint au pixel.
    const atBottom = element.scrollHeight - element.scrollTop - element.clientHeight < 2 * LINE_HEIGHT
    setFollowing(atBottom)
  }, [])

  const items = virtualizer.getVirtualItems()
  const summary = useMemo(() => `${lines.length} ligne${lines.length > 1 ? 's' : ''}`, [lines.length])

  if (loading) return <Skeleton className="h-96 w-full" />
  if (error) return <ErrorState message={error} />

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-xs text-zinc-500">{summary}</span>

        {!complete && (
          <button
            type="button"
            onClick={() => void loadEarlier()}
            className="rounded-md border border-zinc-200 px-2 py-1 text-xs font-medium text-zinc-600 transition-colors hover:bg-zinc-100"
          >
            Charger le début
          </button>
        )}

        <label className="ml-auto flex items-center gap-2 text-xs text-zinc-600">
          <input
            type="checkbox"
            checked={following}
            onChange={(event) => setFollowing(event.target.checked)}
            className="size-3.5"
          />
          Suivre la fin
        </label>

        {sealed && (
          <span className="flex items-center gap-1 text-xs text-zinc-400">
            <Icon name="check" className="size-3.5" />
            journal scellé
          </span>
        )}
      </div>

      <div
        ref={containerRef}
        onScroll={onScroll}
        // `tabIndex` rend la zone atteignable au clavier : sans lui, un journal
        // ne se parcourt qu'à la souris.
        tabIndex={0}
        role="log"
        aria-label={`Journal de ${taskName}`}
        // Le fond suit le thème. Il ne le suivait pas : l'échelle zinc est
        // **inversée** en thème sombre — c'est ainsi que le thème est construit —
        // et un `bg-zinc-950` écrit pour obtenir une console noire en thème clair
        // devenait donc presque blanc en thème sombre. Les tokens ordinaires
        // donnent le bon sens dans les deux.
        className="h-96 overflow-auto rounded-lg border border-zinc-200 bg-zinc-100 font-mono text-xs"
      >
        <div style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
          {items.map((item) => {
            const line = lines[item.index]
            if (!line) return null

            const highlighted = anchor === item.index
            return (
              <div
                key={item.key}
                data-index={item.index}
                // La mesure passe par cette référence : sans elle, le
                // virtualiseur en resterait à son estimation, et les lignes
                // repliées se recouvriraient.
                ref={virtualizer.measureElement}
                style={{
                  position: 'absolute',
                  top: 0,
                  left: 0,
                  width: '100%',
                  transform: `translateY(${item.start}px)`,
                }}
                className={`flex gap-3 px-3 leading-5 ${highlighted ? 'bg-zinc-200' : ''}`}
              >
                <button
                  type="button"
                  onClick={() => {
                    // Le clic pose l'ancre sans repositionner : l'utilisateur
                    // voit déjà la ligne. Il coupe en revanche le suivi, sans
                    // quoi la vue redescendrait au premier événement reçu.
                    setFollowing(false)
                    // `replace` : ancrer une ligne n'est pas une navigation, et
                    // empiler une entrée d'historique par clic rendrait le
                    // bouton « retour » inutilisable.
                    navigate(`#L${item.index + 1}`, { replace: true })
                  }}
                  title={line.timestamp || undefined}
                  aria-label={`Ancrer la ligne ${item.index + 1}`}
                  className="w-12 shrink-0 select-none text-right text-zinc-400 tabular-nums hover:text-zinc-900"
                >
                  {item.index + 1}
                </button>
                {/* `min-w-0` est indispensable dans un conteneur flex : sans
                    lui, la boîte du texte prend la largeur de son contenu au
                    lieu de se replier, et une ligne longue déborde du cadre au
                    lieu de passer à la ligne. */}
                <span className="min-w-0 whitespace-pre-wrap break-all text-zinc-900">
                  {line.text}
                </span>
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}

/** `#L42` désigne la 42e ligne affichée, donc l'indice 41. */
function parseAnchor(hash: string): number | null {
  const match = /^#L(\d+)$/.exec(hash)
  if (!match?.[1]) return null

  const line = Number.parseInt(match[1], 10)
  return Number.isFinite(line) && line > 0 ? line - 1 : null
}
