import { Card, EmptyState, Icon, SIGNAL_LABEL, Skeleton, StatusBadge, formatDateTime, hasAtLeast, useSession, useTests } from '@arkhitekton/host'
import { useState } from 'react'
import { signoffStyle, type SignoffScale as Scale } from './signoffStyle'
import {
  useClearSignoff,
  useSetSignoff,
  useSignoffs,
  type EnvironmentSignoffs,
  type Signoff,
  type SignoffStatus,
} from './api'

/*
 * Validation de recette.
 *
 * Doc 01 §1.2 décrit le sign-off comme la concaténation de deux moitiés : les
 * contrôles automatiques — « CI / Pipelines » — et la validation humaine —
 * « Client ». Les deux sont réunies ici, et l'ordre n'est pas indifférent :
 * l'automatique d'abord, parce qu'un client qui valide le fait au vu des tests.
 *
 * La validation porte sur le couple release-environnement. La maquette l'attachait
 * à l'environnement seul, ses données étant figées ; attachée ainsi, elle
 * survivrait au déploiement suivant et afficherait « validé par le client » pour
 * une version que le client n'a jamais vue.
 */
export function SignoffPanel({ tag, environment }: { tag: string; environment?: string }) {
  const { data, isPending } = useSignoffs(tag)
  const session = useSession()
  const canDecide = session.kind === 'authenticated' && hasAtLeast(session.identity.role, 'operator')

  if (isPending) return <Skeleton className="h-48 w-full" />
  if (!data) return null

  // Sur l'écran d'un environnement, seul celui-ci a du sens. Sur celui d'une
  // release, le flux entier : la question posée y est « où en est-on ? ».
  const shown = environment
    ? data.environments.filter((entry) => entry.environment === environment)
    : data.environments

  return (
    <Card title="Validation de recette">
      <AutomaticChecks tag={tag} />

      <div className="mt-4 flex flex-col gap-4 border-t border-zinc-200 pt-4">
        {shown.length === 0 ? (
          <EmptyState>Aucun environnement à valider.</EmptyState>
        ) : (
          shown.map((entry) => (
            <EnvironmentBlock
              key={entry.environment}
              tag={tag}
              entry={entry}
              canDecide={canDecide}
            />
          ))
        )}
      </div>
    </Card>
  )
}

/**
 * Moitié automatique : ce que la CI a constaté.
 *
 * Résumée et non détaillée. Le détail existe déjà dans le panneau des tests, et le
 * répéter ici ferait deux tableaux à tenir d'accord. Ce qu'on veut à cet endroit,
 * c'est de quoi décider : combien de familles, combien en échec.
 */
function AutomaticChecks({ tag }: { tag: string }) {
  const { data, isPending } = useTests(tag)

  if (isPending) return <Skeleton className="h-12 w-full" />

  const results = data?.results ?? []

  if (results.length === 0) {
    return (
      <div className="flex items-baseline gap-3 text-sm">
        <span className="min-w-32 font-medium text-zinc-900">CI / Pipelines</span>
        {/* Pas de résultat n'est pas un échec : la release peut n'avoir pas
            encore été testée. Le dire ainsi évite qu'on la croie mauvaise. */}
        <span className="text-zinc-500">Aucun résultat de test enregistré pour cette release.</span>
      </div>
    )
  }

  const failed = results.filter((result) => result.status === 'fail')
  const scale: Scale = failed.length > 0 ? 'fail' : 'pass'

  return (
    <div className="flex flex-wrap items-baseline gap-3 text-sm">
      <span className="min-w-32 font-medium text-zinc-900">CI / Pipelines</span>
      <StatusBadge style={signoffStyle(scale)} />
      <span className="text-zinc-500">
        {results.length} famille(s)
        {failed.length > 0 && ` — ${failed.map((r) => SIGNAL_LABEL[r.group] ?? r.group).join(', ')} en échec`}
      </span>
    </div>
  )
}

function EnvironmentBlock({
  tag,
  entry,
  canDecide,
}: {
  tag: string
  entry: EnvironmentSignoffs
  canDecide: boolean
}) {
  return (
    <div>
      <h3 className="text-sm font-medium text-zinc-900">{entry.label}</h3>

      {entry.signoffs.length === 0 ? (
        <p className="mt-1 text-sm text-zinc-500">
          Personne ne s'est encore prononcé sur cette release dans cet environnement.
        </p>
      ) : (
        <ul className="mt-1 flex flex-col">
          {entry.signoffs.map((signoff) => (
            <SignoffRow
              key={signoff.role}
              tag={tag}
              environment={entry.environment}
              signoff={signoff}
              canDecide={canDecide}
            />
          ))}
        </ul>
      )}

      {canDecide && <DecisionForm tag={tag} environment={entry.environment} />}
    </div>
  )
}

function SignoffRow({
  tag,
  environment,
  signoff,
  canDecide,
}: {
  tag: string
  environment: string
  signoff: Signoff
  canDecide: boolean
}) {
  const clear = useClearSignoff(tag)

  return (
    <li className="flex flex-wrap items-baseline gap-x-3 gap-y-1 border-t border-zinc-200 py-1.5 text-sm first:border-t-0">
      <span className="min-w-32 font-medium text-zinc-900">{signoff.role}</span>
      <StatusBadge style={signoffStyle(signoff.status)} />
      {signoff.detail && <span className="text-zinc-600">{signoff.detail}</span>}
      <span className="ml-auto text-zinc-500">
        {signoff.decided_by} — {formatDateTime(signoff.decided_at)}
      </span>
      {canDecide && (
        <button
          type="button"
          onClick={() => clear.mutate({ environment, role: signoff.role })}
          disabled={clear.isPending}
          aria-label={`Retirer l'avis de ${signoff.role}`}
          className="rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-40"
        >
          Retirer
        </button>
      )}
    </li>
  )
}

const DECISIONS: { value: SignoffStatus; label: string }[] = [
  { value: 'pass', label: 'Validé' },
  { value: 'warn', label: 'Réserve' },
  { value: 'fail', label: 'Refusé' },
]

function DecisionForm({ tag, environment }: { tag: string; environment: string }) {
  const set = useSetSignoff(tag)
  const [role, setRole] = useState('')
  const [decision, setDecision] = useState<SignoffStatus>('pass')
  const [detail, setDetail] = useState('')

  // La même règle que l'API, et non à sa place : un motif obligatoire vérifié ici
  // évite un aller-retour, mais c'est le serveur qui tranche (doc 07 §7.2).
  const needsDetail = decision !== 'pass'
  const ready = role.trim() !== '' && (!needsDetail || detail.trim() !== '')

  const submit = () => {
    if (!ready) return
    set.mutate(
      { environment, role: role.trim(), status: decision, detail: detail.trim() },
      {
        onSuccess: () => {
          setRole('')
          setDetail('')
          setDecision('pass')
        },
      },
    )
  }

  return (
    <form
      onSubmit={(event) => {
        event.preventDefault()
        submit()
      }}
      className="mt-2 flex flex-wrap items-start gap-2 border-t border-zinc-200 pt-2"
    >
      <input
        value={role}
        onChange={(event) => setRole(event.target.value)}
        placeholder="Qui se prononce ?"
        aria-label={`Rôle qui se prononce sur ${environment}`}
        className="w-44 rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm text-zinc-900"
      />
      <select
        value={decision}
        onChange={(event) => setDecision(event.target.value as SignoffStatus)}
        aria-label={`Avis sur ${environment}`}
        className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm text-zinc-900"
      >
        {DECISIONS.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
      <input
        value={detail}
        onChange={(event) => setDetail(event.target.value)}
        placeholder={needsDetail ? 'Motif (obligatoire)' : 'Commentaire'}
        aria-label={`Motif de l'avis sur ${environment}`}
        className="min-w-48 flex-1 rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm text-zinc-900"
      />
      <button
        type="submit"
        disabled={!ready || set.isPending}
        className="rounded-md border border-zinc-900 bg-zinc-900 px-3 py-1 text-sm font-medium text-white transition-colors hover:bg-zinc-700 disabled:cursor-not-allowed disabled:opacity-40"
      >
        Enregistrer
      </button>

      {set.isError && (
        <p className="flex w-full items-center gap-1.5 text-sm text-critical-700">
          <Icon name="alert" className="size-4 shrink-0" />
          L'avis n'a pas pu être enregistré.
        </p>
      )}
    </form>
  )
}
