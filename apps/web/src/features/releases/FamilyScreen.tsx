import { useState } from 'react'
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
 */
export function FamilyScreen() {
  const { tag, family } = useParams()
  const { data, isPending, error, refetch } = useFamily(tag, family)
  const [query, setQuery] = useState('')

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
            title={`Cas de test (${data.cases.length})`}
            action={
              <input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Filtrer…"
                aria-label="Filtrer les cas de test"
                className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm text-zinc-900"
              />
            }
          >
            <CaseList cases={data.cases} query={query} />
          </Card>
        </>
      )}
    </div>
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

function CaseList({ cases, query }: { cases: TestCase[]; query: string }) {
  const needle = query.trim().toLowerCase()
  const shown = needle
    ? cases.filter((entry) => `${entry.suite} ${entry.name}`.toLowerCase().includes(needle))
    : cases

  if (shown.length === 0) {
    return <EmptyState>{needle ? 'Aucun cas ne correspond au filtre.' : 'Aucun cas enregistré.'}</EmptyState>
  }

  return (
    <ul className="flex flex-col">
      {shown.map((entry) => (
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
