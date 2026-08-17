import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router'
import { Icon } from '../../components/Icon'
import { Card, EmptyState, ErrorState, Skeleton, StatusBadge } from '../../components/primitives'
import { status } from '../../lib/status'
import { CHANGE_LABEL, FAMILY_LABEL, useFamily, type CaseDiff, type TestCase } from './tests'

/*
 * Détail d'une famille de tests.
 *
 * La comparaison à la release précédente vient **avant** la liste des cas, et
 * c'est délibéré : la question posée en recette est « qu'est-ce qui a changé ? »,
 * pas « combien de tests passent ». Un compteur stable peut cacher un échec
 * résolu et un échec apparu, qui s'annulent au total et n'ont rien à voir.
 *
 * Le filtre et la pagination sont côté serveur (doc 08 §8.6). Une suite Cypress
 * dépasse le millier de cas, et filtrer la seule page transférée répondrait
 * « aucun cas ne correspond » pour un test présent deux pages plus loin.
 */
export function FamilyScreen() {
  const { tag, family } = useParams()
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

  const label = FAMILY_LABEL[family ?? ''] ?? family

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-baseline gap-3">
        <h1 className="text-2xl font-semibold text-zinc-900">{label}</h1>
        <Link to={`/releases/${encodeURIComponent(tag ?? '')}`} className="font-mono text-sm text-zinc-500 hover:underline">
          {tag}
        </Link>
      </div>

      {isPending && <Skeleton className="h-64 w-full" />}
      {error && <ErrorState message="Les résultats n'ont pas pu être chargés." onRetry={() => void refetch()} />}

      {data && (
        <>
          <DiffCard diff={data.diff} />

          <Card
            title={`Cas de test (${data.total})`}
            action={
              <input
                value={input}
                onChange={(event) => setInput(event.target.value)}
                placeholder="Filtrer…"
                aria-label="Filtrer les cas de test"
                className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm text-zinc-900"
              />
            }
          >
            <CaseList cases={data.cases} filtered={needle !== ''} />
            <Pagination
              total={data.total}
              limit={data.limit}
              offset={data.offset}
              onChange={setOffset}
            />
          </Card>
        </>
      )}
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
    <nav aria-label="Pagination des cas de test" className="mt-3 flex items-center gap-3 border-t border-zinc-200 pt-3 text-sm">
      <span className="text-zinc-500">
        {first}–{last} sur {total}
      </span>
      <button
        type="button"
        onClick={() => onChange(Math.max(offset - limit, 0))}
        disabled={offset === 0}
        className="ml-auto rounded-md border border-zinc-300 bg-white px-2 py-1 font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:cursor-not-allowed disabled:opacity-40"
      >
        Précédents
      </button>
      <button
        type="button"
        onClick={() => onChange(offset + limit)}
        disabled={last >= total}
        className="rounded-md border border-zinc-300 bg-white px-2 py-1 font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:cursor-not-allowed disabled:opacity-40"
      >
        Suivants
      </button>
    </nav>
  )
}

const CHANGE_TONE: Record<string, string> = {
  'new-failure': 'text-critical-700',
  fixed: 'text-healthy-700',
  added: 'text-zinc-600',
  removed: 'text-warn-700',
}

function DiffCard({ diff }: { diff?: CaseDiff[] }) {
  if (!diff) {
    // Pas de release précédente : ce n'est pas une erreur, la première release
    // d'un projet n'a rien à quoi se comparer.
    return (
      <Card title="Comparaison">
        <EmptyState>Aucune release antérieure à laquelle comparer cette famille.</EmptyState>
      </Card>
    )
  }

  if (diff.length === 0) {
    return (
      <Card title="Comparaison">
        <EmptyState>Rien n'a changé depuis la release précédente.</EmptyState>
      </Card>
    )
  }

  // L'ordre importe : un nouvel échec se lit avant un test ajouté.
  const order = ['new-failure', 'fixed', 'removed', 'added']
  const sorted = [...diff].sort((a, b) => order.indexOf(a.change) - order.indexOf(b.change))

  return (
    <Card title={`Comparaison à la release précédente (${diff.length})`}>
      <ul className="flex flex-col gap-1">
        {sorted.map((entry) => (
          <li
            key={`${entry.change}-${entry.suite}-${entry.name}`}
            className="flex flex-wrap items-baseline gap-x-3 gap-y-1 border-t border-zinc-200 py-1.5 text-sm first:border-t-0"
          >
            <span className={`min-w-28 font-medium ${CHANGE_TONE[entry.change] ?? 'text-zinc-600'}`}>
              {CHANGE_LABEL[entry.change]}
            </span>
            <span className="font-mono text-zinc-900">{entry.name}</span>
            <span className="text-zinc-500">{entry.suite}</span>
            {entry.was && entry.status && (
              <span className="ml-auto flex items-center gap-1 text-zinc-500">
                {entry.was}
                <Icon name="arrow-right" className="size-3.5" />
                {entry.status}
              </span>
            )}
          </li>
        ))}
      </ul>
    </Card>
  )
}

const CASE_STATUS = {
  passed: 'pass',
  failed: 'fail',
  skipped: 'skip',
  flaky: 'warn',
} as const

// Le filtrage a lieu côté serveur : cette liste rend ce qu'on lui donne. C'est ce
// qui permet à « aucun cas ne correspond » de porter sur la famille entière et non
// sur la page affichée.
function CaseList({ cases, filtered }: { cases: TestCase[]; filtered: boolean }) {
  if (cases.length === 0) {
    return <EmptyState>{filtered ? 'Aucun cas ne correspond au filtre.' : 'Aucun cas enregistré.'}</EmptyState>
  }

  return (
    <ul className="flex flex-col">
      {cases.map((entry) => (
        <CaseRow key={`${entry.suite}::${entry.name}`} entry={entry} />
      ))}
    </ul>
  )
}

function CaseRow({ entry }: { entry: TestCase }) {
  const [open, setOpen] = useState(false)
  const hasDetail = Boolean(entry.trace)

  return (
    <li className="border-t border-zinc-200 py-1.5 text-sm first:border-t-0">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <StatusBadge style={status.pipeline(CASE_STATUS[entry.status])} />
        <span className="font-mono text-zinc-900">{entry.name}</span>
        <span className="text-zinc-500">{entry.suite}</span>
        {entry.message && <span className="text-zinc-600">{entry.message}</span>}
        {entry.duration_ms > 0 && <span className="ml-auto text-zinc-500">{entry.duration_ms} ms</span>}
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
