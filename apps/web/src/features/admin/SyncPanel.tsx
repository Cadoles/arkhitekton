import { useState } from 'react'
import { Icon } from '../../components/Icon'
import { Card, EmptyState, Skeleton } from '../../components/primitives'
import { formatDateTime } from '../../lib/format'
import {
  useForceResync,
  useSetStatusBucket,
  useSyncAdmin,
  type Bucket,
  type SyncAdmin,
} from '../versions/api'

/*
 * État de la synchronisation Redmine, et ce qui demande une décision humaine.
 *
 * Trois signaux, et aucun n'est une panne du dashboard :
 *
 *   - des statuts sans projection déclarée, qui font dériver le taux de complétion
 *     sans rien signaler ;
 *   - des versions dont plusieurs noms se réduisent au même numéro, ce qui empêche
 *     de rattacher une release automatiquement ;
 *   - des versions provisoires jamais adoptées, symptôme d'une faute de frappe dans
 *     un tag ou d'une version absente de Redmine.
 *
 * Les afficher est ce qui les rend corrigeables.
 */
export function SyncPanel() {
  const { data, isPending } = useSyncAdmin()

  if (isPending) return <Skeleton className="h-48 w-full" />
  if (!data) return null

  if (!data.configured) {
    return (
      <Card title="Synchronisation Redmine">
        {/* Non configuré et jamais synchronisé n'appellent pas la même réaction :
            le second ferait chercher une panne. */}
        <EmptyState>
          Redmine n'est pas configuré. Renseignez <span className="font-mono">REDMINE_URL</span> et{' '}
          <span className="font-mono">REDMINE_API_KEY</span> pour renseigner les versions et les
          demandes.
        </EmptyState>
      </Card>
    )
  }

  return (
    <Card title="Synchronisation Redmine" action={<ResyncButton />}>
      <SyncSummary data={data} />
      <UnmappedStatuses data={data} />
      <MappingTable data={data} />
      <AmbiguousVersions data={data} />
      <PendingVersions data={data} />
    </Card>
  )
}

function ResyncButton() {
  const resync = useForceResync()

  return (
    <div className="flex items-center gap-2">
      {resync.isSuccess && (
        <span className="text-xs text-zinc-500">{resync.data?.message}</span>
      )}
      <button
        type="button"
        onClick={() => resync.mutate()}
        disabled={resync.isPending}
        className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-40"
      >
        Resynchroniser
      </button>
    </div>
  )
}

function SyncSummary({ data }: { data: SyncAdmin }) {
  return (
    <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
      <dt className="text-zinc-500">Dernière tentative</dt>
      <dd className="text-zinc-900">
        {data.sync.last_run ? formatDateTime(data.sync.last_run) : 'jamais'}
      </dd>
      <dt className="text-zinc-500">Dernier succès</dt>
      <dd className="text-zinc-900">
        {data.sync.last_ok ? formatDateTime(data.sync.last_ok) : 'jamais'}
      </dd>
      {data.sync.last_error && (
        <>
          <dt className="text-zinc-500">Dernière erreur</dt>
          <dd className="text-critical-700">{data.sync.last_error}</dd>
        </>
      )}
    </dl>
  )
}

const BUCKETS: { value: Bucket; label: string }[] = [
  { value: 'todo', label: 'À faire' },
  { value: 'doing', label: 'En cours' },
  { value: 'done', label: 'Terminé' },
  { value: 'rejected', label: 'Écarté' },
]

/**
 * Statuts rencontrés sans projection déclarée.
 *
 * Ils tombent dans « à faire » en attendant. Les statuts Redmine sont propres au
 * workflow du client et changent sans prévenir ; un ajout non projeté ferait dériver
 * le taux de complétion sans que rien ne le signale.
 */
function UnmappedStatuses({ data }: { data: SyncAdmin }) {
  if (data.unmapped.length === 0) return null

  return (
    <div className="mt-4 border-t border-zinc-200 pt-3">
      <h3 className="flex items-center gap-1.5 text-sm font-medium text-warn-700">
        <Icon name="alert" className="size-4" />
        {data.unmapped.length} statut(s) sans projection déclarée
      </h3>
      <p className="mt-1 text-sm text-zinc-500">
        Rangés dans « à faire » en attendant. Tant qu'ils ne sont pas déclarés, le taux de
        complétion les compte comme non commencés.
      </p>
      <ul className="mt-2 flex flex-col gap-1">
        {data.unmapped.map((entry) => (
          <StatusRow
            key={entry.status_name}
            status={entry.status_name}
            issues={entry.issues}
            current={undefined}
          />
        ))}
      </ul>
    </div>
  )
}

/*
 * Statuts dont la projection attend une décision du client.
 *
 * Ils sont placés en « en cours » par la migration 0012, par prudence : livré n'est
 * pas nécessairement recetté, et surestimer la complétion annoncerait une version
 * complète alors que la recette n'a pas eu lieu.
 *
 * Cette liste double celle de la migration, et c'est assumé : elle ne sert qu'à
 * mettre en avant les lignes à regarder. Une divergence n'aurait pour effet que de
 * souligner la mauvaise ligne, jamais de fausser un calcul.
 */
const AWAITING_VALIDATION = new Set([
  'Livré',
  'Livré DEV',
  'Prêt à livrer',
  'Testé',
  'Installation',
])

/**
 * Table complète de la projection, éditable.
 *
 * Les statuts **déjà projetés** ont besoin de cette interface autant que les
 * inconnus, et plus encore : ce sont eux qui décident du taux de complétion affiché
 * au client, et cinq d'entre eux attendent son arbitrage. N'exposer que les statuts
 * non mappés laissait le réglage inaccessible dès que l'amorçage les couvrait tous,
 * ce qui est le cas sur l'instance réelle.
 */
function MappingTable({ data }: { data: SyncAdmin }) {
  const [open, setOpen] = useState(false)

  const statuses = Object.keys(data.mapping).sort((a, b) => a.localeCompare(b, 'fr'))
  const awaiting = statuses.filter((status) => AWAITING_VALIDATION.has(status))

  return (
    <div className="mt-4 border-t border-zinc-200 pt-3">
      <div className="flex flex-wrap items-baseline gap-3">
        <h3 className="text-sm font-medium text-zinc-900">
          Projection des statuts ({statuses.length})
        </h3>
        <button
          type="button"
          onClick={() => setOpen((value) => !value)}
          aria-expanded={open}
          className="rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
        >
          {open ? 'Replier' : 'Modifier'}
        </button>
      </div>

      <p className="mt-1 text-sm text-zinc-500">
        Chaque statut Redmine est rangé dans un seau. Le taux de complétion vaut « terminé »
        divisé par « tout sauf écarté » : changer un seau déplace donc un chiffre affiché.
      </p>

      {awaiting.length > 0 && (
        <p className="mt-2 flex items-start gap-2 rounded-md border border-warn-500 bg-warn-50 px-3 py-2 text-sm text-warn-700">
          <Icon name="alert" className="mt-0.5 size-4 shrink-0" />
          <span>
            {awaiting.length} statut(s) placé(s) en « en cours » par prudence, en attente d'un
            arbitrage : <span className="font-medium">{awaiting.join(', ')}</span>. Livré n'est pas
            nécessairement recetté — surestimer la complétion annoncerait une version complète
            alors que la recette n'a pas eu lieu.
          </span>
        </p>
      )}

      {open && (
        <ul className="mt-2 flex flex-col gap-1">
          {statuses.map((status) => (
            <StatusRow
              // Le seau courant entre dans la clé : une modification enregistrée
              // remonte la table, et sans cela la ligne conserverait l'état local
              // de son sélecteur, affichant une valeur que le serveur a remplacée.
              key={`${status}:${data.mapping[status]}`}
              status={status}
              current={data.mapping[status]}
              flagged={AWAITING_VALIDATION.has(status)}
            />
          ))}
        </ul>
      )}
    </div>
  )
}

function StatusRow({
  status,
  issues,
  current,
  flagged,
}: {
  status: string
  issues?: number
  current: Bucket | undefined
  flagged?: boolean
}) {
  const setBucket = useSetStatusBucket()
  const [bucket, setLocal] = useState<Bucket>(current ?? 'todo')

  return (
    <li className="flex flex-wrap items-center gap-2 text-sm">
      <span className={`min-w-48 ${flagged ? 'font-medium text-warn-700' : 'text-zinc-900'}`}>
        {status}
      </span>
      {issues !== undefined && <span className="text-zinc-500">{issues} demande(s)</span>}
      <select
        value={bucket}
        onChange={(event) => setLocal(event.target.value as Bucket)}
        aria-label={`Projection du statut ${status}`}
        className="rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-sm text-zinc-900"
      >
        {BUCKETS.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
      <button
        type="button"
        onClick={() => setBucket.mutate({ status, bucket })}
        disabled={setBucket.isPending}
        className="rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-40"
      >
        Déclarer
      </button>
      {/* Le nombre déplacé est rendu par le serveur : déclarer un statut réapplique
          la règle aux demandes déjà enregistrées, et le silence laisserait croire
          que seules les futures sont concernées. */}
      {setBucket.isSuccess && (
        <span className="text-xs text-zinc-500">
          {setBucket.data?.issues_moved} demande(s) reclassée(s)
        </span>
      )}
    </li>
  )
}

/**
 * Versions dont plusieurs noms se réduisent au même numéro.
 *
 * Ce n'est pas une erreur du dashboard mais un fait du projet — `MSE 3.1.2` et
 * `v3.1.2` coexistent — et c'est pourtant lui qui empêche de rattacher une release
 * automatiquement. D'où l'affichage explicite plutôt qu'un choix arbitraire.
 */
function AmbiguousVersions({ data }: { data: SyncAdmin }) {
  if (data.ambiguous.length === 0) return null

  return (
    <div className="mt-4 border-t border-zinc-200 pt-3">
      <h3 className="flex items-center gap-1.5 text-sm font-medium text-warn-700">
        <Icon name="alert" className="size-4" />
        {data.ambiguous.length} numéro(s) porté(s) par plusieurs versions
      </h3>
      <p className="mt-1 text-sm text-zinc-500">
        Une release portant ce numéro ne peut pas être rattachée automatiquement : le dashboard ne
        choisit pas à votre place.
      </p>
      <ul className="mt-2 flex flex-col gap-1 text-sm">
        {data.ambiguous.map((entry) => (
          <li key={entry.normalized} className="flex flex-wrap items-baseline gap-2">
            <span className="font-mono text-zinc-900">{entry.normalized}</span>
            <span className="text-zinc-500">{entry.names.join(' · ')}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}

/**
 * Versions provisoires jamais adoptées.
 *
 * Une provisoire qui subsiste désigne une release rattachée à une version que Redmine
 * ne connaît pas : faute de frappe dans le tag, version supprimée, ou ambiguïté de
 * nommage.
 */
function PendingVersions({ data }: { data: SyncAdmin }) {
  if (data.pending.length === 0) return null

  return (
    <div className="mt-4 border-t border-zinc-200 pt-3">
      <h3 className="text-sm font-medium text-zinc-900">
        {data.pending.length} version(s) inconnue(s) de Redmine
      </h3>
      <p className="mt-1 text-sm text-zinc-500">
        Créées par une release. Aucune version Redmine ne porte ce numéro — vérifiez le tag, ou la
        liste des ambiguïtés ci-dessus.
      </p>
      <ul className="mt-2 flex flex-wrap gap-2 font-mono text-sm text-zinc-600">
        {data.pending.map((name) => (
          <li key={name} className="rounded bg-zinc-100 px-1.5 py-0.5">
            {name}
          </li>
        ))}
      </ul>
    </div>
  )
}
