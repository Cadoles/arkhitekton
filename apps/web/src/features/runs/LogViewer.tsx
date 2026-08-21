import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useLocation, useNavigate } from 'react-router'
import { useVirtualizer } from '@tanstack/react-virtual'
import { APP_SCROLL_ID } from '../../components/AppShell'
import { Icon } from '../../components/Icon'
import { ErrorState, Skeleton } from '../../components/primitives'
import { useScrollLock } from '../../lib/scrollLock'
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
 *   - **l'ancrage** sur une ligne ou une plage, partageable par URL, qui doit
 *     aussi l'interrompre.
 *
 * La virtualisation a un prix, et il faut l'assumer : la recherche du navigateur
 * ne trouve que ce qui est rendu, et « tout sélectionner » ne prend que la
 * fenêtre visible. D'où les deux échappatoires — le texte brut dans un onglet et
 * le téléchargement — qui servent précisément les usages que la virtualisation
 * empêche.
 */

const LINE_HEIGHT = 20

/**
 * Hauteur plancher, celle d'avant. Sur un grand écran la visionneuse s'étire
 * jusqu'au bas de la fenêtre ; en dessous de cette taille elle cesserait de se
 * lire comme un journal, et vaut mieux alors déborder que rétrécir.
 */
const MIN_HEIGHT = 384

/** Marge sous la visionneuse quand elle s'étire, pour ne pas coller au bord. */
const BOTTOM_GUTTER = 24

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
  const [expanded, setExpanded] = useState(false)

  // L'ancre vient du routeur et non de `window.location`. La différence n'est pas
  // théorique : le composant est monté après la résolution de la requête, et lire
  // l'objet global à ce moment-là ne donnait rien — le routeur est la seule source
  // qui reste juste quelle que soit la séquence de rendu.
  // L'ancre a une source unique : l'URL. Un état local en parallèle finirait par
  // la contredire — un clic ancien resterait prioritaire sur un lien fraîchement
  // ouvert, et rien dans l'interface ne l'expliquerait.
  const { hash, search } = useLocation()
  const navigate = useNavigate()
  const selection = useMemo(() => parseAnchor(hash), [hash])
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
    // Elle était autrefois imposée, et c'était le défaut : une ligne trop longue
    // pour la largeur se replie sur plusieurs hauteurs de texte, mais la suivante
    // restait positionnée à une hauteur de ligne de distance — les lignes se
    // recouvraient. `measureElement`, posé en référence sur chaque ligne rendue,
    // corrige la position des suivantes.
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

    if (selection !== null && !positioned.current) {
      positioned.current = true
      setFollowing(false)
      virtualizerRef.current.scrollToIndex(Math.min(selection.start, lines.length - 1), {
        align: 'center',
      })
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
  }, [selection, following, lines.length])

  const onScroll = useCallback(() => {
    const element = containerRef.current
    if (!element) return

    // Une tolérance est indispensable : les hauteurs estimées par le
    // virtualiseur font que le bas exact n'est presque jamais atteint au pixel.
    const atBottom = element.scrollHeight - element.scrollTop - element.clientHeight < 2 * LINE_HEIGHT
    setFollowing(atBottom)
  }, [])

  // Sélection d'une ligne, ou extension de la plage en cours avec Maj.
  //
  // Le geste est celui des forges, et c'est délibéré : personne n'apprendra une
  // convention propre à ce dashboard pour désigner vingt lignes d'erreur.
  const select = useCallback(
    (index: number, extend: boolean) => {
      // Le clic coupe le suivi, sans quoi la vue redescendrait au premier
      // événement reçu — et le lecteur perdrait la ligne qu'il vient de viser.
      setFollowing(false)

      const line = index + 1
      const anchor = extend && selection ? `#L${selection.start + 1}-L${line}` : `#L${line}`
      // La query est reconduite explicitement, et c'est indispensable : elle
      // porte la task dont on lit le journal. Naviguer vers la seule ancre la
      // ferait disparaître — react-router résout `#L42` en une destination sans
      // query — et le lien partagé rouvrirait la task par défaut, c'est-à-dire
      // une autre, avec un numéro de ligne qui n'y désigne rien.
      //
      // `replace` : ancrer une ligne n'est pas une navigation, et empiler une
      // entrée d'historique par clic rendrait le bouton « retour » inutilisable.
      navigate({ search, hash: anchor }, { replace: true })
    },
    [navigate, search, selection],
  )

  useScrollLock(expanded)

  // Échap referme le plein écran. Sans lui, la seule sortie serait un bouton
  // qu'on ne cherche pas quand la vue occupe l'écran entier.
  useEffect(() => {
    if (!expanded) return
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setExpanded(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [expanded])

  // Mesurée sur le panneau lui-même : c'est sa position dans la page qui dit
  // combien de place reste en dessous.
  //
  // `loading` fait partie de la condition, et pas par prudence : pendant le
  // chargement le panneau n'est pas monté, la mesure ne trouve rien, et sans ce
  // passage de faux à vrai elle ne serait jamais refaite — la visionneuse
  // resterait au plancher sur un grand écran.
  const height = useAvailableHeight(containerRef, !expanded && !loading)

  const items = virtualizer.getVirtualItems()
  const summary = useMemo(() => `${lines.length} ligne${lines.length > 1 ? 's' : ''}`, [lines.length])

  if (loading) return <Skeleton className="h-96 w-full" />
  if (error) return <ErrorState message={error} />

  const view = (
    <div className={`flex flex-col gap-2 ${expanded ? 'min-h-0 flex-1' : ''}`}>
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

        {selection && <ShareLink selection={selection} />}

        <span className="ml-auto" />
        <Tools runName={runName} taskName={taskName} expanded={expanded} onExpand={setExpanded} />
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
        className={`overflow-auto rounded-lg border border-zinc-200 bg-zinc-100 font-mono text-xs ${
          expanded ? 'min-h-0 flex-1' : ''
        }`}
        style={expanded ? undefined : { height }}
      >
        <div style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
          {items.map((item) => {
            const line = lines[item.index]
            if (!line) return null

            const highlighted =
              selection !== null && item.index >= selection.start && item.index <= selection.end
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
                // `items-start` place le numéro en face de la **première** ligne
                // d'un texte replié. Par défaut une rangée flex étire ses enfants
                // et le bouton, centrant son contenu, affichait le numéro au
                // milieu du bloc — à côté de nulle part.
                className={`flex items-start gap-3 px-3 leading-5 ${highlighted ? 'bg-zinc-200' : ''}`}
              >
                <button
                  type="button"
                  onClick={(event) => select(item.index, event.shiftKey)}
                  title={line.timestamp || undefined}
                  aria-label={`Ancrer la ligne ${item.index + 1}`}
                  className="w-12 shrink-0 select-none text-right leading-5 text-zinc-400 tabular-nums hover:text-zinc-900"
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

      {/* Le suivi se commande sous le journal, à hauteur de la fin qu'il colle.
          En tête de panneau, la case se lisait comme un réglage de la barre
          d'outils alors qu'elle décrit ce que fait le bas de la vue. */}
      <div className="flex flex-wrap items-center gap-3">
        <label className="flex items-center gap-2 text-xs text-zinc-600">
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
    </div>
  )

  if (!expanded) return view

  // Le plein écran passe par un portail : la visionneuse est rendue au fond
  // d'une grille et de deux cartes, et un `fixed` posé là serait contenu par le
  // premier ancêtre transformé rencontré. Le portail la sort de cette pile.
  //
  // Une superposition maison plutôt que l'API Fullscreen du navigateur : celle-ci
  // sort l'élément du document et lui donne un fond noir imposé, ce qui casse le
  // thème et le lien avec le reste de l'écran.
  return createPortal(
    <div className="fixed inset-0 z-40 flex flex-col gap-2 bg-zinc-50 p-4">
      <p className="font-mono text-xs text-zinc-500">
        {runName} — {taskName}
      </p>
      {view}
    </div>,
    document.body,
  )
}

/** Bouton de copie du lien vers la sélection. */
function ShareLink({ selection }: { selection: Selection }) {
  const [copied, setCopied] = useState(false)

  // Le retour visuel s'efface de lui-même : un « copié » qui reste laisse croire,
  // à la copie suivante, que rien ne s'est passé.
  useEffect(() => {
    if (!copied) return
    const timer = window.setTimeout(() => setCopied(false), 2000)
    return () => window.clearTimeout(timer)
  }, [copied])

  const label =
    selection.start === selection.end
      ? `ligne ${selection.start + 1}`
      : `lignes ${selection.start + 1} à ${selection.end + 1}`

  return (
    <button
      type="button"
      onClick={() => {
        // L'URL vient de la barre d'adresse et non d'une reconstruction : elle
        // porte déjà l'ancre que la sélection vient d'y écrire, et la recomposer
        // à la main ferait diverger les deux au premier changement de route.
        void navigator.clipboard
          .writeText(window.location.href)
          .then(() => setCopied(true))
          .catch(() => setCopied(false))
      }}
      title="Copier le lien vers cette sélection"
      className="flex items-center gap-1 rounded-md border border-zinc-200 px-2 py-1 text-xs font-medium text-zinc-600 transition-colors hover:bg-zinc-100"
    >
      <Icon name={copied ? 'check' : 'link'} className="size-3.5" />
      {copied ? 'Lien copié' : `Copier le lien — ${label}`}
    </button>
  )
}

/** Sorties du journal : texte brut, fichier, plein écran. */
function Tools({
  runName,
  taskName,
  expanded,
  onExpand,
}: {
  runName: string
  taskName: string
  expanded: boolean
  onExpand: (expanded: boolean) => void
}) {
  // Route servie par `internal/http/logs.go`. Une navigation ordinaire suffit à
  // l'atteindre : la session est un cookie, et ces liens visent la même origine.
  const raw = `/api/v1/runs/${encodeURIComponent(runName)}/tasks/${encodeURIComponent(taskName)}/log.txt`

  return (
    <span className="flex items-center gap-1">
      <a
        href={raw}
        target="_blank"
        rel="noreferrer"
        title="Ouvrir le journal brut dans un onglet"
        className="flex items-center gap-1 rounded-md border border-zinc-200 px-2 py-1 text-xs font-medium text-zinc-600 transition-colors hover:bg-zinc-100"
      >
        <Icon name="external" className="size-3.5" />
        Brut
      </a>
      <a
        href={`${raw}?download=1`}
        title="Télécharger le journal complet"
        className="flex items-center gap-1 rounded-md border border-zinc-200 px-2 py-1 text-xs font-medium text-zinc-600 transition-colors hover:bg-zinc-100"
      >
        <Icon name="download" className="size-3.5" />
        Télécharger
      </a>
      <button
        type="button"
        onClick={() => onExpand(!expanded)}
        title={expanded ? 'Quitter le plein écran (Échap)' : 'Afficher en plein écran'}
        aria-pressed={expanded}
        className="flex items-center gap-1 rounded-md border border-zinc-200 px-2 py-1 text-xs font-medium text-zinc-600 transition-colors hover:bg-zinc-100"
      >
        <Icon name={expanded ? 'collapse' : 'expand'} className="size-3.5" />
        {expanded ? 'Réduire' : 'Plein écran'}
      </button>
    </span>
  )
}

/**
 * Hauteur restante pour la visionneuse, plancher compris.
 *
 * Une hauteur fixe gâche un grand écran : la moitié basse reste vide pendant
 * qu'on fait défiler quarante lignes dans une fenêtre de vingt. La mesure est
 * faite sur la position réelle du panneau plutôt que déduite d'une chaîne de
 * `h-full` — celle-ci traverserait la coquille, deux cartes et une grille, et
 * casserait au premier changement de mise en page.
 *
 * Ce qui compte n'est pas la distance au bas de la **fenêtre** mais la place
 * libre dans le conteneur de défilement. Viser le bas de la fenêtre laissait
 * dehors tout ce qui vit sous le panneau — la marge de la carte, celle de la
 * page, la ligne de commande du suivi — et le journal étiré poussait donc la
 * page d'autant : il créait l'ascenseur qu'il devait justement rendre inutile.
 *
 * D'où la forme retenue : `disponible = visible − occupé au-dessus − occupé en
 * dessous`, les deux occupations étant des distances entre le panneau et les
 * bords du contenu. Ni l'une ni l'autre ne dépend de la hauteur du panneau, donc
 * une seule passe suffit et le résultat tient exactement dans la fenêtre.
 *
 * Recalculée au redimensionnement seulement, pas au défilement : la hauteur du
 * panneau ne dépend pas de l'endroit où l'on est dans la page, et la recalculer
 * pendant qu'on défile la ferait vibrer.
 */
function useAvailableHeight(ref: React.RefObject<HTMLElement | null>, enabled: boolean): number {
  const [height, setHeight] = useState(MIN_HEIGHT)

  useLayoutEffect(() => {
    if (!enabled) return

    const measure = () => {
      const element = ref.current
      if (!element) return
      setHeight(Math.max(MIN_HEIGHT, Math.round(availableHeight(element))))
    }

    measure()
    window.addEventListener('resize', measure)
    return () => window.removeEventListener('resize', measure)
  }, [enabled, ref])

  return height
}

function availableHeight(element: HTMLElement): number {
  const panel = element.getBoundingClientRect()

  const scroller = document.getElementById(APP_SCROLL_ID)
  // Le contenu est l'enfant du conteneur de défilement, et c'est lui qu'il faut
  // mesurer : `scrollHeight` est écrêté à la hauteur visible quand le contenu
  // est plus court, ce qui ferait conclure qu'il n'y a pas de place à prendre.
  const content = scroller?.firstElementChild
  if (!scroller || !content) {
    // Hors coquille — un écran rendu seul. Le bas de la fenêtre est alors le
    // seul repère disponible.
    return window.innerHeight - panel.top - BOTTOM_GUTTER
  }

  const box = content.getBoundingClientRect()
  return scroller.clientHeight - (panel.top - box.top) - (box.bottom - panel.bottom)
}

/**
 * `#L42` désigne la 42e ligne affichée, donc l'indice 41 ; `#L42-L50` la plage
 * qui va de la 42e à la 50e.
 */
type Selection = { start: number; end: number }

function parseAnchor(hash: string): Selection | null {
  const match = /^#L(\d+)(?:-L(\d+))?$/.exec(hash)
  if (!match?.[1]) return null

  const first = Number.parseInt(match[1], 10)
  if (!Number.isFinite(first) || first <= 0) return null

  const second = match[2] ? Number.parseInt(match[2], 10) : first
  if (!Number.isFinite(second) || second <= 0) return null

  // Les bornes sont réordonnées : une sélection étendue vers le haut produit
  // `#L50-L42`, qui désigne la même plage et doit se lire pareil.
  return { start: Math.min(first, second) - 1, end: Math.max(first, second) - 1 }
}
