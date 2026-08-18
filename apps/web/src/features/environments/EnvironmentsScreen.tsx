import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Icon } from '../../components/Icon'
import { Card, EmptyState, ErrorState, Skeleton } from '../../components/primitives'
import { formatDateTime } from '../../lib/format'
import { usePromote, useReleases, type Release } from '../releases/api'
import { describe, useEnvironments, type Environment } from './api'

/*
 * Vue d'ensemble des environnements.
 *
 * L'écran répond à deux questions et pas une de plus : **qu'est-ce qui tourne
 * où**, et **qu'est-ce qui a été demandé sans être encore arrivé**. La seconde est
 * la moins évidente et la plus utile — une promotion réussie ne déploie rien par
 * elle-même, elle pousse un tag que Flux constate ensuite (B7).
 */
export function EnvironmentsScreen() {
  const { data, isPending, error, refetch } = useEnvironments()
  const releases = useReleases()

  // Les environnements arrivent déjà dans l'ordre de promotion. Le conserver
  // n'est pas cosmétique : c'est le chemin qu'une version parcourt.
  const environments = useMemo(() => data?.environments ?? [], [data])

  // Seules les releases **validées** sont promouvables (doc 04 §4.2). `ready`
  // signifie « tests terminés, décision en attente » : proposer la promotion à
  // ce stade court-circuiterait précisément la décision que le cycle réserve à
  // un humain.
  const promotable = useMemo(
    () => (releases.data?.releases ?? []).filter((release) => release.state === 'released'),
    [releases.data],
  )

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h1 className="text-2xl font-semibold text-zinc-900">Environnements</h1>
        <p className="text-sm text-zinc-500">Dans l'ordre de promotion.</p>
      </div>

      {isPending && <Skeleton className="h-64 w-full" />}
      {error && <ErrorState message="Les environnements n'ont pas pu être chargés." onRetry={() => void refetch()} />}

      {data && environments.length === 0 && (
        <EmptyState>Aucun environnement déclaré dans le catalogue.</EmptyState>
      )}

      <div className="grid gap-4 md:grid-cols-2">
        {environments.map((environment) => (
          <EnvironmentCard key={environment.id} environment={environment} releases={promotable} />
        ))}
      </div>
    </div>
  )
}

const TONE = {
  ok: { text: 'text-healthy-700', icon: 'check' },
  warn: { text: 'text-warn-700', icon: 'clock' },
  neutral: { text: 'text-zinc-500', icon: 'minus' },
} as const

function EnvironmentCard({ environment, releases }: { environment: Environment; releases: Release[] }) {
  const summary = describe(environment)
  const tone = TONE[summary.tone]

  return (
    <Card
      action={
        <Link to={`/environments/${environment.id}/history`} className="text-xs text-zinc-500 hover:underline">
          Historique
        </Link>
      }
      title={
        <span className="flex items-center gap-2">
          <Link to={`/environments/${environment.id}`} className="hover:underline">
            {environment.label}
          </Link>
          <span className="rounded bg-zinc-100 px-1.5 py-0.5 font-mono text-xs font-normal text-zinc-500">
            {/* Le suffixe est affiché parce qu'il ne se devine pas : Flux connaît
                `qualif` sous le nom `testing`, et c'est ce nom-là qui part dans le
                tag. Le cacher rendrait toute vérification impossible. */}
            {environment.promotion_suffix}
          </span>
        </span>
      }
    >
      <p className={`flex items-start gap-2 text-sm ${tone.text}`}>
        <Icon name={tone.icon} className="mt-0.5 size-4 shrink-0" />
        {summary.text}
      </p>

      <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
        {environment.portal_version && (
          <>
            <dt className="text-zinc-500">Version</dt>
            <dd className="font-mono text-zinc-900">{environment.portal_version}</dd>
          </>
        )}
        {environment.migration_state && (
          <>
            <dt className="text-zinc-500">Migrations</dt>
            <dd className="text-zinc-900">{environment.migration_state}</dd>
          </>
        )}
        {environment.flux_state && (
          <>
            <dt className="text-zinc-500">Flux</dt>
            <dd className="text-zinc-900">{environment.flux_state}</dd>
          </>
        )}
        {environment.observed_at && (
          <>
            <dt className="text-zinc-500">Constaté</dt>
            <dd className="text-zinc-600">{formatDateTime(environment.observed_at)}</dd>
          </>
        )}
      </dl>

      {environment.last_promotion && (
        <p className="mt-3 border-t border-zinc-200 pt-3 text-sm text-zinc-600">
          Dernière promotion : <span className="font-mono">{environment.last_promotion.tag}</span> —{' '}
          {formatDateTime(environment.last_promotion.pushed_at)}
        </p>
      )}

      {environment.can_deploy && <PromotePanel environment={environment} releases={releases} />}
    </Card>
  )
}

function PromotePanel({ environment, releases }: { environment: Environment; releases: Release[] }) {
  const [selected, setSelected] = useState('')
  const [confirming, setConfirming] = useState(false)
  const promote = usePromote()

  // Toutes les releases prêtes sont proposées, et c'est le serveur qui refuse.
  // Rejouer ici la règle SemVer donnerait une interface qui autorise ce que l'API
  // refuse — ou l'inverse — dès que la plage d'une ImagePolicy change. Le refus
  // arrive avec son motif, ce qui vaut mieux qu'un choix absent sans explication.

  return (
    <form
      className="mt-3 flex flex-wrap items-center gap-2 border-t border-zinc-200 pt-3"
      onSubmit={(event) => {
        event.preventDefault()
        // Jamais de promotion directe : la confirmation rappelle l'environnement,
        // la version et l'action (doc 08 §8.5).
        if (selected) setConfirming(true)
      }}
    >
      {confirming && (
        <ConfirmDialog
          title="Promouvoir cette release ?"
          confirmLabel="Promouvoir"
          pending={promote.isPending}
          /* Un environnement sensible exige la ressaisie de la version. Le coût
             de la friction est négligeable devant celui d'une erreur. */
          challenge={environment.sensitive ? selected : undefined}
          onCancel={() => setConfirming(false)}
          onConfirm={() => {
            promote.mutate({ tag: selected, environment: environment.id })
            setConfirming(false)
          }}
        >
          <p>
            Release <span className="font-mono font-medium text-zinc-900">{selected}</span>
          </p>
          <p>
            Environnement <span className="font-medium text-zinc-900">{environment.label}</span>
          </p>
          <p className="text-zinc-500">
            Le tag <span className="font-mono">{environment.promotion_suffix}</span> sera poussé dans le registre. Le
            déploiement suivra la réconciliation de Flux.
          </p>
        </ConfirmDialog>
      )}
      <label className="sr-only" htmlFor={`promote-${environment.id}`}>
        Release à promouvoir vers {environment.label}
      </label>
      <select
        id={`promote-${environment.id}`}
        value={selected}
        onChange={(event) => setSelected(event.target.value)}
        className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm text-zinc-900"
      >
        <option value="">Choisir une release…</option>
        {releases.map((release) => (
          <option key={release.tag} value={release.tag}>
            {release.tag}
          </option>
        ))}
      </select>

      <button
        type="submit"
        disabled={!selected || promote.isPending}
        className="rounded-md border border-zinc-300 bg-white px-3 py-1 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-50"
      >
        Promouvoir
      </button>

      {promote.isError && (
        // Le motif du refus vient du serveur et il est affiché tel quel : c'est
        // lui qui sait qu'une pré-release n'atteindra jamais la production, et le
        // reformuler ici perdrait la seule information utile.
        <p className="w-full text-sm text-critical-700">{(promote.error as Error).message}</p>
      )}
      {promote.isSuccess && (
        <p className="w-full text-sm text-zinc-600">
          Promotion mise en file — <span className="font-mono">{promote.data.tag}</span>. Le déploiement suivra la
          réconciliation de Flux.
        </p>
      )}
    </form>
  )
}
