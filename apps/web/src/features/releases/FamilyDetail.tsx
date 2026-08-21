import { useEffect, useState } from 'react'
import { Icon } from '../../components/Icon'
import { EmptyState, ErrorState, Skeleton, StatusBadge } from '../../components/primitives'
import { status } from '../../lib/status'
import { PipelineTimeline } from './PipelineTimeline'
import { CHANGE_LABEL, useFamily, type CaseDiff, type Change, type TestCase } from './tests'

/*
 * Détail d'une famille de tests, sans son cadre.
 *
 * Extrait pour servir deux fois : la page `/releases/:tag/tests/:family` et la
 * modale ouverte depuis une tuile du score. Les deux montrent exactement la même
 * chose — dupliquer la liste des cas les aurait fait diverger au premier
 * ajustement.
 *
 * La comparaison à la release précédente ne fait plus une section à elle. Elle
 * tient en une ligne de comptes au-dessus, et en une pastille sur les cas
 * concernés : la question posée en recette est « qu'est-ce qui a changé ? », et
 * y répondre dans un bloc séparé obligeait à faire la jointure de tête entre
 * deux listes qui portent les mêmes noms.
 */

const CASE_STATUS = {
  passed: 'pass',
  failed: 'fail',
  skipped: 'skip',
  flaky: 'warn',
} as const

/** Pastille courte, à mettre sur un cas. Le libellé long reste pour l'infobulle. */
const CHANGE_BADGE: Record<Change, { label: string; tone: string }> = {
  'new-failure': { label: 'nouveau', tone: 'border-critical-500 bg-critical-50 text-critical-700' },
  fixed: { label: 'corrigé', tone: 'border-healthy-500 bg-healthy-50 text-healthy-700' },
  added: { label: 'ajouté', tone: 'border-zinc-300 bg-zinc-100 text-zinc-600' },
  removed: { label: 'retiré', tone: 'border-warn-500 bg-warn-50 text-warn-700' },
}

/** Ordre de gravité : un nouvel échec se lit avant un test ajouté. */
const CHANGE_ORDER: Change[] = ['new-failure', 'fixed', 'removed', 'added']

function caseKey(suite: string, name: string): string {
  return `${suite}::${name}`
}

export function FamilyDetail({
  tag,
  family,
  /** Run en cours mesurant cette famille, s'il y en a un. */
  runName,
}: {
  tag: string | undefined
  family: string | undefined
  runName?: string
}) {
  const [input, setInput] = useState('')
  const [needle, setNeedle] = useState('')
  const [offset, setOffset] = useState(0)

  // Le filtre part au serveur après une pause de frappe : une requête par
  // caractère saturerait la base pour des résultats que personne ne lit.
  useEffect(() => {
    const timer = setTimeout(() => {
      setNeedle(input.trim())
      // Retour à la première page : rester à l'offset 500 d'un filtre qui ne rend
      // que douze cas afficherait une page vide, ce qui se lit comme « aucun
      // résultat ».
      setOffset(0)
    }, 300)
    return () => clearTimeout(timer)
  }, [input])

  const { data, isPending, error, refetch } = useFamily(tag, family, offset, needle)

  const changes = new Map<string, Change>()
  for (const entry of data?.diff ?? []) changes.set(caseKey(entry.suite, entry.name), entry.change)

  return (
    <div className="flex flex-col gap-3">
      {/* La frise avant tout le reste : quand un pipeline tourne, ce qui suit
          décrit la mesure précédente, et savoir où il en est vaut mieux que de
          lire attentivement un résultat périmé. */}
      {runName && <PipelineTimeline runName={runName} />}

      {isPending && <Skeleton className="h-48 w-full" />}
      {error && (
        <ErrorState
          message="Les résultats n'ont pas pu être chargés."
          onRetry={() => void refetch()}
        />
      )}

      {data && (
        <>
          <DiffSummary diff={data.diff} />

          <div className="flex flex-wrap items-center justify-between gap-2">
            <h3 className="text-sm font-semibold text-zinc-900">Cas de test ({data.total})</h3>
            <input
              value={input}
              onChange={(event) => setInput(event.target.value)}
              placeholder="Filtrer…"
              aria-label="Filtrer les cas de test"
              className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm text-zinc-900"
            />
          </div>

          <CaseList cases={data.cases} filtered={needle !== ''} changes={changes} />

          <Pagination
            total={data.total}
            limit={data.limit}
            offset={data.offset}
            onChange={setOffset}
          />

          {/* Les cas disparus n'ont pas de ligne dans la liste : ils ne sont plus
              exécutés. Les taire ferait passer une suppression de test pour une
              amélioration du taux de réussite. */}
          <RemovedCases diff={data.diff} />
        </>
      )}
    </div>
  )
}

/**
 * Comparaison à la release précédente, en une ligne de comptes.
 *
 * Les comptes et non la liste : le détail vit sur les cas eux-mêmes, en
 * pastille. Un compteur stable peut cacher un échec résolu et un échec apparu,
 * qui s'annulent au total et n'ont rien à voir — d'où le décompte par nature
 * plutôt qu'un delta unique.
 */
export function DiffSummary({ diff }: { diff?: CaseDiff[] }) {
  if (!diff) {
    // Pas de release précédente : ce n'est pas une erreur, la première release
    // d'un projet n'a rien à quoi se comparer.
    return (
      <p className="text-sm text-zinc-500">
        Aucune release antérieure à laquelle comparer cette famille.
      </p>
    )
  }

  if (diff.length === 0) {
    return <p className="text-sm text-zinc-500">Rien n&apos;a changé depuis la release précédente.</p>
  }

  const counts = new Map<Change, number>()
  for (const entry of diff) counts.set(entry.change, (counts.get(entry.change) ?? 0) + 1)

  return (
    <p className="flex flex-wrap items-center gap-2 text-sm">
      <span className="text-zinc-500">Depuis la release précédente</span>
      {CHANGE_ORDER.filter((change) => counts.has(change)).map((change) => (
        <span
          key={change}
          title={CHANGE_LABEL[change]}
          className={`inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium ${CHANGE_BADGE[change].tone}`}
        >
          {counts.get(change)} {CHANGE_BADGE[change].label}
          {(counts.get(change) ?? 0) > 1 ? 'x' : ''}
        </span>
      ))}
    </p>
  )
}

/**
 * Les cas qui ont disparu depuis la release précédente.
 *
 * Ils n'apparaissent pas dans la liste — le rapport courant ne les contient
 * plus — et sans cette mention, supprimer un test en échec se lirait comme
 * l'avoir corrigé.
 */
function RemovedCases({ diff }: { diff?: CaseDiff[] }) {
  const removed = (diff ?? []).filter((entry) => entry.change === 'removed')
  if (removed.length === 0) return null

  return (
    <div className="rounded-md border border-warn-500 bg-warn-50 px-3 py-2 text-sm text-warn-700">
      <p className="flex items-center gap-2 font-medium">
        <Icon name="alert" className="size-4 shrink-0" />
        {removed.length} cas ne sont plus exécutés
      </p>
      <ul className="mt-1 flex flex-col gap-0.5">
        {removed.map((entry) => (
          <li key={caseKey(entry.suite, entry.name)} className="font-mono text-xs">
            {entry.name} <span className="font-sans text-warn-700/70">{entry.suite}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}

/**
 * Navigation entre les pages de cas.
 *
 * Absente quand tout tient sur une page, ce qui est le cas courant : afficher
 * « 1–8 sur 8 » et deux boutons inertes ajouterait du bruit sans rien apprendre.
 */
function Pagination({
  total,
  limit,
  offset,
  onChange,
}: {
  total: number
  limit: number
  offset: number
  onChange: (offset: number) => void
}) {
  if (total <= limit) return null

  const first = offset + 1
  const last = Math.min(offset + limit, total)

  return (
    <nav
      aria-label="Pagination des cas de test"
      className="flex items-center gap-3 border-t border-zinc-200 pt-3 text-sm"
    >
      <span className="text-zinc-500">
        {first}–{last} sur {total}
      </span>
      <button
        type="button"
        disabled={offset === 0}
        onClick={() => onChange(Math.max(0, offset - limit))}
        className="rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-sm text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-40"
      >
        Précédents
      </button>
      <button
        type="button"
        disabled={last >= total}
        onClick={() => onChange(offset + limit)}
        className="rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-sm text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-40"
      >
        Suivants
      </button>
    </nav>
  )
}

// Le filtrage a lieu côté serveur : cette liste rend ce qu'on lui donne. C'est ce
// qui permet à « aucun cas ne correspond » de porter sur la famille entière et non
// sur la page affichée.
function CaseList({
  cases,
  filtered,
  changes,
}: {
  cases: TestCase[]
  filtered: boolean
  changes: Map<string, Change>
}) {
  if (cases.length === 0) {
    return (
      <EmptyState>
        {filtered ? 'Aucun cas ne correspond au filtre.' : 'Aucun cas enregistré.'}
      </EmptyState>
    )
  }

  return (
    <ul className="flex flex-col">
      {cases.map((entry) => (
        <CaseRow
          key={caseKey(entry.suite, entry.name)}
          entry={entry}
          change={changes.get(caseKey(entry.suite, entry.name))}
        />
      ))}
    </ul>
  )
}

function CaseRow({ entry, change }: { entry: TestCase; change?: Change }) {
  const [open, setOpen] = useState(false)
  const hasDetail = Boolean(entry.trace)
  const badge = change ? CHANGE_BADGE[change] : undefined

  return (
    <li className="border-t border-zinc-200 py-1.5 text-sm first:border-t-0">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <StatusBadge style={status.pipeline(CASE_STATUS[entry.status])} />

        {/* La pastille de changement à côté du statut, pas en fin de ligne :
            c'est elle qu'on cherche en recette, et une ligne longue la
            repousserait hors du regard. */}
        {badge && (
          <span
            title={CHANGE_LABEL[change as Change]}
            className={`inline-flex items-center rounded-full border px-1.5 py-0.5 text-xs font-medium ${badge.tone}`}
          >
            {badge.label}
          </span>
        )}

        <span className="font-mono text-zinc-900">{entry.name}</span>
        <span className="text-zinc-500">{entry.suite}</span>
        {entry.message && <span className="text-zinc-600">{entry.message}</span>}
        {entry.duration_ms > 0 && (
          <span className="ml-auto text-zinc-500">{entry.duration_ms} ms</span>
        )}
        {hasDetail && (
          <button
            type="button"
            onClick={() => setOpen((value) => !value)}
            aria-expanded={open}
            className="rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
          >
            {open ? 'Replier' : 'Trace'}
          </button>
        )}
      </div>

      {/* La trace est repliée par défaut : elle fait dix lignes par cas, et un
          tableau qui les déplie toutes cesse d'être un tableau. */}
      {open && entry.trace && (
        <pre className="mt-2 overflow-x-auto rounded-md border border-zinc-200 bg-zinc-50 p-2 font-mono text-xs text-zinc-700">
          {entry.trace}
        </pre>
      )}
    </li>
  )
}
