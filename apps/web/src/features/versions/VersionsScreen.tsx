import { Link } from 'react-router'
import { Icon } from '../../components/Icon'
import { Card, EmptyState, ErrorState, Skeleton } from '../../components/primitives'
import { formatDateTime } from '../../lib/format'
import { useVersions, type Completion, type SyncState, type Version } from './api'

/*
 * Liste des versions déclarées dans Redmine.
 *
 * L'âge des données est affiché avec elles : Redmine est un service tiers qui peut ne
 * pas répondre, et un écran qui montre des chiffres sans dire de quand ils datent est
 * trompeur (doc 08 §8.2).
 */
export function VersionsScreen() {
  const { data, isPending, error, refetch } = useVersions()

  return (
    <div className="flex flex-col gap-4">
      <h1 className="text-2xl font-semibold text-zinc-900">Versions</h1>

      {isPending && <Skeleton className="h-64 w-full" />}
      {error && <ErrorState message="Les versions n'ont pas pu être chargées." onRetry={() => void refetch()} />}

      {data && (
        <>
          <SyncBanner sync={data.sync} />

          {data.versions.length === 0 ? (
            <Card title="Versions">
              <EmptyState>
                {data.sync.ever_ran
                  ? 'Aucune version déclarée dans le projet Redmine.'
                  : "La synchronisation Redmine n'a pas encore tourné."}
              </EmptyState>
            </Card>
          ) : (
            <ul className="flex flex-col gap-2">
              {data.versions.map((version) => (
                <VersionRow key={version.id} version={version} />
              ))}
            </ul>
          )}
        </>
      )}
    </div>
  )
}

/**
 * Bandeau d'âge et d'état.
 *
 * Trois cas distingués, parce qu'ils n'appellent pas la même réaction : jamais
 * synchronisé, synchronisé mais dernière tentative en échec — les données affichées
 * restent valides, seulement vieillissantes — et tout va bien.
 */
function SyncBanner({ sync }: { sync: SyncState }) {
  if (!sync.ever_ran) {
    return (
      <p className="flex items-start gap-2 rounded-md border border-zinc-200 bg-zinc-50 px-3 py-2 text-sm text-zinc-600">
        <Icon name="clock" className="mt-0.5 size-4 shrink-0" />
        La synchronisation Redmine n'a jamais tourné. Les versions apparaîtront à sa première passe.
      </p>
    )
  }

  if (sync.last_error) {
    return (
      <p className="flex items-start gap-2 rounded-md border border-warn-500 bg-warn-50 px-3 py-2 text-sm text-warn-700">
        <Icon name="alert" className="mt-0.5 size-4 shrink-0" />
        <span>
          Dernière synchronisation en échec. Les données affichées datent du{' '}
          {sync.last_ok ? formatDateTime(sync.last_ok) : 'néant'} et restent exploitables, mais
          n'intègrent pas les modifications faites depuis dans Redmine.
        </span>
      </p>
    )
  }

  return (
    <p className="text-sm text-zinc-500">
      Synchronisé le {sync.last_ok ? formatDateTime(sync.last_ok) : '—'}.
    </p>
  )
}

const STATUS_LABEL: Record<string, string> = {
  open: 'Ouverte',
  locked: 'Verrouillée',
  closed: 'Close',
}

function VersionRow({ version }: { version: Version }) {
  return (
    <li className="rounded-md border border-zinc-200 px-3 py-2">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <Link
          to={`/versions/${version.id}`}
          className="font-mono font-medium text-zinc-900 hover:underline"
        >
          {version.name}
        </Link>
        <span className="text-sm text-zinc-500">
          {STATUS_LABEL[version.status] ?? version.status}
        </span>

        {version.pending && (
          // Le nom vient d'un tag de release, pas de Redmine : la synchronisation
          // n'a trouvé aucune version correspondante.
          <span className="flex items-center gap-1 text-sm text-warn-700">
            <Icon name="alert" className="size-3.5" />
            inconnue de Redmine
          </span>
        )}

        {version.due_date && (
          <span className="text-sm text-zinc-500">échéance {formatDateTime(version.due_date)}</span>
        )}

        {version.releases > 0 && (
          <span className="text-sm text-zinc-500">
            {version.releases} release{version.releases > 1 ? 's' : ''}
          </span>
        )}

        {/* Les anomalies ouvertes sont à droite et distinctes de la jauge : une
            version peut être fonctionnellement complète et en porter, ce sont deux
            signaux qui doivent le rester (doc 04 §4.5). */}
        {version.open_anomalies > 0 && (
          <span className="ml-auto flex items-center gap-1 text-sm text-critical-700">
            <Icon name="alert" className="size-3.5" />
            {version.open_anomalies} anomalie{version.open_anomalies > 1 ? 's' : ''} ouverte
            {version.open_anomalies > 1 ? 's' : ''}
          </span>
        )}
      </div>

      <CompletionBar completion={version.completion} />
    </li>
  )
}

/**
 * Jauge de complétion.
 *
 * La mesure par nombre de demandes est affichée, la pondérée par `done_ratio` en
 * infobulle : la première est lisible, la seconde plus fine mais tributaire de la
 * rigueur de saisie dans Redmine (doc 04 §4.5).
 */
export function CompletionBar({ completion }: { completion: Completion }) {
  if (completion.ratio === null) {
    return (
      <p className="mt-1.5 text-sm text-zinc-500">
        Aucune demande rattachée : rien à mesurer.
      </p>
    )
  }

  const percent = Math.round(completion.ratio * 100)
  const weighted =
    completion.weighted_ratio !== null ? Math.round(completion.weighted_ratio * 100) : null

  return (
    <div
      className="mt-1.5 flex items-center gap-2"
      title={
        weighted !== null
          ? `Pondérée par l'avancement saisi : ${weighted} %`
          : undefined
      }
    >
      <div
        role="progressbar"
        aria-valuenow={percent}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-label="Complétion de la version"
        className="h-1.5 flex-1 overflow-hidden rounded-full bg-zinc-200"
      >
        <div className="h-full rounded-full bg-healthy-500" style={{ width: `${percent}%` }} />
      </div>
      <span className="w-28 text-right text-sm text-zinc-600">
        {completion.done}/{completion.counted} — {percent} %
      </span>
    </div>
  )
}
