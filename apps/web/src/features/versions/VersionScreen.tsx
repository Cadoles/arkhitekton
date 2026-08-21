import { Link, useParams } from 'react-router'
import { Icon } from '../../components/Icon'
import { Card, EmptyState, ErrorState, Skeleton, StatusBadge } from '../../components/primitives'
import { status } from '../../lib/status'
import { formatDateTime } from '../../lib/format'
import { CompletionBar } from './VersionsScreen'
import { RedmineMark } from '../../components/RedmineMark'
import { CreateReleaseButton } from './CreateReleaseButton'
import { useVersion, type Bucket, type Issue, type VersionRelease } from './api'

/*
 * Périmètre d'une version.
 *
 * Trois colonnes — à faire, en cours, terminé — et les anomalies en bandeau séparé,
 * comptées à part. Une version peut être fonctionnellement complète et porter des
 * anomalies ouvertes : deux signaux distincts qui doivent le rester (doc 04 §4.5).
 */
export function VersionScreen() {
  const { id } = useParams()
  const { data, isPending, error, refetch } = useVersion(id)

  return (
    <div className="flex flex-col gap-4">
      {/* Barre de contrôle : le retour à gauche, les actions à droite. Le titre
          reste entre les deux — c'est lui qui dit sur quoi portent les boutons,
          et le reléguer sous la barre séparerait l'action de son objet. */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-3">
          <Link
            to="/versions"
            className="flex items-center gap-1.5 rounded-md border border-zinc-300 bg-white px-2.5 py-1.5 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
          >
            <Icon name="arrow-left" className="size-4" />
            Toutes les versions
          </Link>
          <h1 className="font-mono text-2xl font-semibold text-zinc-900">
            {data?.version.name ?? '…'}
          </h1>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          {data?.version.url && (
            <a
              href={data.version.url}
              target="_blank"
              rel="noreferrer noopener"
              className="flex items-center gap-1.5 rounded-md border border-zinc-300 bg-white px-2.5 py-1.5 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
            >
              <RedmineMark className="size-4" />
              Redmine
              {/* La sortie du dashboard est annoncée : l'onglet change, et rien
                  d'autre dans la barre ne le fait. */}
              <Icon name="external" className="size-3.5 text-zinc-400" />
            </a>
          )}
          {data && <CreateReleaseButton version={data.version} releases={data.releases} />}
        </div>
      </div>

      {isPending && <Skeleton className="h-64 w-full" />}
      {error && <ErrorState message="La version n'a pas pu être chargée." onRetry={() => void refetch()} />}

      {data && (
        <>
          <Card title="Avancement">
            {data.version.pending && (
              <p className="mb-3 flex items-start gap-2 rounded-md border border-warn-500 bg-warn-50 px-3 py-2 text-sm text-warn-700">
                <Icon name="alert" className="mt-0.5 size-4 shrink-0" />
                <span>
                  Cette version vient d'un tag de release et non de Redmine : la synchronisation n'y
                  a trouvé aucune version correspondante. Elle n'a donc ni périmètre ni échéance.
                </span>
              </p>
            )}

            <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
              <dt className="text-zinc-500">Statut</dt>
              <dd className="text-zinc-900">{data.version.status}</dd>
              {data.version.due_date && (
                <>
                  <dt className="text-zinc-500">Échéance</dt>
                  <dd className="text-zinc-900">{formatDateTime(data.version.due_date)}</dd>
                </>
              )}
              <dt className="text-zinc-500">Synchronisé</dt>
              <dd className="text-zinc-600">{formatDateTime(data.version.synced_at)}</dd>
            </dl>

            <CompletionBar completion={data.version.completion} />
          </Card>

          {/* Juste après l'avancement : c'est le résultat de la version, et ce
              qu'on vient chercher le plus souvent. Le laisser sous le périmètre
              le reléguait derrière quatre colonnes de demandes. */}
          <Releases releases={data.releases} />

          <AnomaliesBanner anomalies={data.anomalies} />

          {/* Quatre colonnes dans l'ordre d'avancement. `Test client` s'intercale
              entre « en cours » et « terminé » : le développement y est fini, la
              validation du client non — et cette colonne ne compte pas dans le taux
              de complétion, qui ne retient que « terminé ». */}
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
            <BucketColumn title="À faire" issues={data.buckets.todo} />
            <BucketColumn title="En cours" issues={data.buckets.doing} />
            <BucketColumn
              title="Test client"
              issues={data.buckets['client-test']}
              note="Développement fini, validation du client en attente. Hors du taux de complétion."
            />
            <BucketColumn title="Terminé" issues={data.buckets.done} />
          </div>

          {/* Les rejetées ne sont pas masquées : elles sortent du calcul mais restent
              le périmètre discuté, et les cacher ferait chercher une demande dont on
              sait qu'elle existe. */}
          {data.buckets.rejected.length > 0 && (
            <Card title={`Écartées (${data.buckets.rejected.length})`}>
              <p className="mb-2 text-sm text-zinc-500">
                Hors du calcul de complétion : elles ne pèsent ni au numérateur ni au dénominateur.
              </p>
              <IssueList issues={data.buckets.rejected} />
            </Card>
          )}
        </>
      )}
    </div>
  )
}

// Bandeau des anomalies ouvertes, compté à part du taux de complétion.
function AnomaliesBanner({ anomalies }: { anomalies: Issue[] }) {
  if (anomalies.length === 0) return null

  return (
    <Card title={`Anomalies ouvertes (${anomalies.length})`}>
      <p className="mb-2 text-sm text-zinc-500">
        Comptées séparément : elles n'entrent pas dans le taux de complétion.
      </p>
      <IssueList issues={anomalies} />
    </Card>
  )
}

function BucketColumn({
  title,
  issues,
  note,
}: {
  title: string
  issues: Issue[]
  note?: string
}) {
  return (
    <Card title={`${title} (${issues.length})`}>
      {note && <p className="mb-2 text-xs text-zinc-500">{note}</p>}
      {issues.length === 0 ? (
        <EmptyState>Aucune demande.</EmptyState>
      ) : (
        <IssueList issues={issues} />
      )}
    </Card>
  )
}

function IssueList({ issues }: { issues: Issue[] }) {
  return (
    <ul className="flex flex-col">
      {issues.map((issue) => (
        <li
          key={issue.redmine_id}
          className="border-t border-zinc-200 py-1.5 text-sm first:border-t-0"
        >
          <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
            {issue.url ? (
              <a
                href={issue.url}
                target="_blank"
                rel="noreferrer noopener"
                className="font-mono text-xs text-zinc-500 hover:underline"
              >
                #{issue.redmine_id}
              </a>
            ) : (
              <span className="font-mono text-xs text-zinc-500">#{issue.redmine_id}</span>
            )}
            <span className="text-zinc-900">{issue.subject}</span>
          </div>
          <div className="mt-0.5 flex flex-wrap items-baseline gap-x-2 text-xs text-zinc-500">
            <span>{issue.tracker}</span>
            <span>{issue.status_name}</span>
            {issue.assignee && <span>{issue.assignee}</span>}
            {issue.done_ratio > 0 && <span>{issue.done_ratio} %</span>}
            {issue.anomaly && (
              <span className="flex items-center gap-1 text-critical-700">
                <Icon name="alert" className="size-3" aria-hidden />
                anomalie
              </span>
            )}
          </div>
        </li>
      ))}
    </ul>
  )
}

const KIND_LABEL: Record<string, string> = {
  pre: 'pré-release',
  final: 'finale',
  patch: 'patch',
}

function Releases({ releases }: { releases: VersionRelease[] }) {
  return (
    <Card title={`Releases produites (${releases.length})`}>
      {releases.length === 0 ? (
        <EmptyState>Aucune release produite pour cette version.</EmptyState>
      ) : (
        <ul className="flex flex-col">
          {releases.map((item) => (
            <li
              key={item.tag}
              className="flex flex-wrap items-baseline gap-x-3 gap-y-1 border-t border-zinc-200 py-1.5 text-sm first:border-t-0"
            >
              <Link
                to={`/releases/${encodeURIComponent(item.tag)}`}
                className="font-mono font-medium text-zinc-900 hover:underline"
              >
                {item.tag}
              </Link>
              <span className="text-zinc-500">{KIND_LABEL[item.kind] ?? item.kind}</span>
              {/* L'état passe par le vocabulaire commun : il s'affichait ici tel
                  qu'il est stocké — `released`, `verifying` — alors que les deux
                  autres écrans le traduisent. */}
              <StatusBadge style={status.release(item.state)} />
              {/* L'écart tag / version d'image est le fait d'un re-tag : ce qui tourne
                  porte la version de la RC (B8, B17). */}
              {item.image_version !== item.tag && (
                <span className="text-zinc-500">image {item.image_version}</span>
              )}
              <span className="ml-auto text-zinc-500">{formatDateTime(item.created_at)}</span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  )
}

export type { Bucket }
