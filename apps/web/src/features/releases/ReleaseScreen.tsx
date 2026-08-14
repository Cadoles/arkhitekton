import { useState } from 'react'
import { Link, useParams } from 'react-router'
import { BenchPanel } from './BenchPanel'
import { TestsPanel } from './TestsPanel'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Icon } from '../../components/Icon'
import { Card, EmptyState, ErrorState, Skeleton } from '../../components/primitives'
import { formatDateTime } from '../../lib/format'
import { useEnvironments } from '../environments/api'
import {
  downloadURL,
  useExports,
  useGenerateExport,
  useRelease,
  useSaveManualActions,
  usePromote,
  type Image,
  type PromotionTarget,
  type ReleaseDetail,
} from './api'

/*
 * Agrégat d'une release.
 *
 * L'écran est indexé par release plutôt que par environnement : c'est la même
 * matière que `/environments/:env` de la maquette, vue depuis ce qui est livré
 * plutôt que depuis l'endroit où ça tourne.
 *
 * Les résultats de tests, le score et les anomalies rattachées viendront des
 * lots 4 et 1 ; ce qui est ici est ce que le système sait déjà dire.
 */
export function ReleaseScreen() {
  const { tag } = useParams()
  const { data, isPending, error, refetch } = useRelease(tag)

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-baseline gap-3">
        <h1 className="font-mono text-2xl font-semibold text-zinc-900">{tag}</h1>
        <Link to="/releases" className="text-sm text-zinc-500 hover:underline">
          Toutes les releases
        </Link>
      </div>

      {isPending && <Skeleton className="h-64 w-full" />}
      {error && <ErrorState message="Cette release n'a pas pu être chargée." onRetry={() => void refetch()} />}

      {data && (
        <>
          <Identity release={data} />
          <Images release={data} />
          <Deployment release={data} />
          <TestsPanel tag={data.tag} />
          <BenchPanel tag={data.tag} />
          <Executions release={data} />
          <ManualActionsPanel release={data} />
          <Exports release={data} />
        </>
      )}
    </div>
  )
}

function Identity({ release }: { release: ReleaseDetail }) {
  return (
    <Card title="Identité">
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
        <dt className="text-zinc-500">Nature</dt>
        <dd className="text-zinc-900">{release.kind}</dd>
        <dt className="text-zinc-500">État</dt>
        <dd className="text-zinc-900">{release.state}</dd>
        <dt className="text-zinc-500">Version d'image</dt>
        <dd className="font-mono text-zinc-900">{release.image_version}</dd>
        {release.git_ref && (
          <>
            <dt className="text-zinc-500">Référence Git</dt>
            <dd className="font-mono text-zinc-900">{release.git_ref}</dd>
          </>
        )}
        <dt className="text-zinc-500">Créée</dt>
        <dd className="text-zinc-900">
          {release.created_by} — {formatDateTime(release.created_at)}
        </dd>
      </dl>

      {release.image_version !== release.tag && (
        // Le point le plus contre-intuitif de tout le dispositif : ce qui part en
        // production porte encore la version de la RC. Le taire ferait conclure à
        // une erreur de déploiement en lisant l'écran des environnements.
        <p className="mt-3 flex items-start gap-2 rounded-md border border-zinc-200 px-3 py-2 text-sm text-zinc-600">
          <Icon name="alert" className="mt-0.5 size-4 shrink-0 text-warn-700" />
          <span>
            Cette release est un re-tag de <span className="font-mono">{release.image_version}</span> : l'image est bit
            pour bit celle qui a été validée, et son <span className="font-mono">version.txt</span> porte donc encore la
            version de la pré-release. C'est cette valeur, et non le tag, que rapportera le portail.
          </span>
        </p>
      )}
    </Card>
  )
}

function Images({ release }: { release: ReleaseDetail }) {
  const images = release.images ?? []
  // Le digest de référence est celui de l'image construite, en tête de liste.
  const reference = images[0]?.digest

  return (
    <Card title="Images">
      {release.images_error && <p className="mb-3 text-sm text-zinc-500">{release.images_error}</p>}

      {images.length === 0 ? (
        <EmptyState>Aucune image connue pour cette release.</EmptyState>
      ) : (
        <ul className="flex flex-col gap-2">
          {images.map((image) => (
            <ImageRow key={image.tag} image={image} reference={reference} />
          ))}
        </ul>
      )}
    </Card>
  )
}

function ImageRow({ image, reference }: { image: Image; reference?: string }) {
  // Un digest différent de celui de l'image construite signale une image
  // reconstruite entre-temps : ce qui est déployé n'est plus ce qui a été validé.
  const diverged = Boolean(reference && image.digest && image.digest !== reference)

  return (
    <li className="flex flex-wrap items-baseline gap-x-3 gap-y-1 rounded-md border border-zinc-200 px-3 py-2 text-sm">
      <span className="font-mono text-zinc-900">
        {image.repository}:{image.tag}
      </span>
      {image.digest ? (
        <span className={`font-mono text-xs ${diverged ? 'text-critical-700' : 'text-zinc-500'}`}>{image.digest}</span>
      ) : (
        <span className="text-xs text-zinc-500">tag non poussé</span>
      )}
      {diverged && (
        <span className="flex items-center gap-1 text-critical-700">
          <Icon name="alert" className="size-3.5" />
          digest différent de l'image construite
        </span>
      )}
    </li>
  )
}

function Deployment({ release }: { release: ReleaseDetail }) {
  const environments = useEnvironments()
  const promote = usePromote()
  const [target, setTarget] = useState<PromotionTarget | null>(null)

  const promoted = new Map((release.promotions ?? []).map((promotion) => [promotion.environment, promotion]))
  const sensitive = new Set(
    (environments.data?.environments ?? []).filter((entry) => entry.sensitive).map((entry) => entry.id),
  )

  return (
    <Card title="Déploiement">
      {(release.targets ?? []).length === 0 ? (
        <EmptyState>Aucun environnement déclaré.</EmptyState>
      ) : (
        <ul className="flex flex-col gap-2">
          {(release.targets ?? []).map((entry) => {
            const done = promoted.get(entry.environment)
            return (
              <li
                key={entry.environment}
                className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border border-zinc-200 px-3 py-2 text-sm"
              >
                <span className="font-medium text-zinc-900">{entry.label}</span>

                {done ? (
                  <span className="text-zinc-500">
                    promue <span className="font-mono">{done.tag}</span> — {formatDateTime(done.pushed_at)}
                  </span>
                ) : entry.allowed ? (
                  <span className="font-mono text-zinc-500">{entry.tag}</span>
                ) : (
                  // Le motif accompagne toujours le refus : un bouton grisé sans
                  // explication se lit comme une panne.
                  <span className="text-zinc-500">{entry.reason}</span>
                )}

                {entry.allowed && (
                  <button
                    type="button"
                    onClick={() => setTarget(entry)}
                    className="ml-auto rounded-md border border-zinc-300 bg-white px-3 py-1 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
                  >
                    {done ? 'Repromouvoir' : 'Promouvoir'}
                  </button>
                )}
              </li>
            )
          })}
        </ul>
      )}

      {promote.isError && <p className="mt-3 text-sm text-critical-700">{(promote.error as Error).message}</p>}

      {target && (
        <ConfirmDialog
          title="Promouvoir cette release ?"
          confirmLabel="Promouvoir"
          pending={promote.isPending}
          challenge={sensitive.has(target.environment) ? release.tag : undefined}
          onCancel={() => setTarget(null)}
          onConfirm={() => {
            promote.mutate({ tag: release.tag, environment: target.environment })
            setTarget(null)
          }}
        >
          <p>
            Release <span className="font-mono font-medium text-zinc-900">{release.tag}</span>
          </p>
          <p>
            Environnement <span className="font-medium text-zinc-900">{target.label}</span>
          </p>
          <p className="text-zinc-500">
            Le tag <span className="font-mono">{target.tag}</span> sera poussé dans le registre. Le déploiement suivra
            la réconciliation de Flux.
          </p>
        </ConfirmDialog>
      )}
    </Card>
  )
}

function Executions({ release }: { release: ReleaseDetail }) {
  const jobs = release.jobs ?? []

  return (
    <Card title="Exécutions">
      {jobs.length === 0 ? (
        <EmptyState>Aucune exécution déclenchée pour cette release.</EmptyState>
      ) : (
        <ul className="flex flex-col gap-2">
          {jobs.map((job) => (
            <li
              key={job.id}
              className="flex flex-wrap items-baseline gap-x-3 gap-y-1 rounded-md border border-zinc-200 px-3 py-2 text-sm"
            >
              <span className="font-medium text-zinc-900">{job.kind}</span>
              {job.environment && <span className="text-zinc-500">{job.environment}</span>}
              <span className="text-zinc-500">{job.state}</span>
              {/* Le lien vers le run est le chemin entre une promotion et sa
                  preuve : c'est là que sont les journaux. */}
              {job.run_name && (
                <Link to={`/runs/${encodeURIComponent(job.run_name)}`} className="font-mono text-zinc-600 hover:underline">
                  {job.run_name}
                </Link>
              )}
              <span className="ml-auto text-zinc-500">
                {job.requested_by} — {formatDateTime(job.requested_at)}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  )
}

function ManualActionsPanel({ release }: { release: ReleaseDetail }) {
  const stored = release.manual_actions?.markdown ?? ''

  // La clé remonte l'éditeur quand le texte enregistré change, et seulement
  // alors. Une saisie en cours ne bouge donc pas — `stored` ne varie pas pendant
  // qu'on tape — mais une sauvegarde, ici ou ailleurs, repart du texte réel.
  // Synchroniser dans un effet ferait la même chose avec un rendu de plus et une
  // fenêtre où les deux valeurs divergent.
  return <ManualActionsEditor key={stored} release={release} stored={stored} />
}

function ManualActionsEditor({ release, stored }: { release: ReleaseDetail; stored: string }) {
  const save = useSaveManualActions(release.tag)
  const [draft, setDraft] = useState(stored)
  const dirty = draft !== stored

  return (
    <Card
      title="Actions manuelles"
      action={
        release.manual_actions?.updated_at ? (
          <span className="text-xs text-zinc-500">
            {release.manual_actions.updated_by} — {formatDateTime(release.manual_actions.updated_at)}
          </span>
        ) : undefined
      }
    >
      {/* Champ libre et non liste de cases à cocher : les gestes manuels d'une
          livraison ne se répètent pas assez d'une version à l'autre pour qu'une
          checklist les couvre, et une checklist incomplète se remplit quand même.
          Ce texte est repris dans le bon de livraison. */}
      <label className="sr-only" htmlFor="manual-actions">
        Actions manuelles de la livraison
      </label>
      <textarea
        id="manual-actions"
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
        rows={8}
        placeholder="Gestes manuels de la livraison, en Markdown. Repris dans le bon de livraison."
        className="w-full rounded-md border border-zinc-300 bg-white px-3 py-2 font-mono text-sm text-zinc-900"
      />

      <div className="mt-2 flex items-center gap-3">
        <button
          type="button"
          onClick={() => save.mutate(draft)}
          disabled={!dirty || save.isPending}
          className="rounded-md border border-zinc-300 bg-white px-3 py-1 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-50"
        >
          Enregistrer
        </button>
        {save.isError && <span className="text-sm text-critical-700">{(save.error as Error).message}</span>}
        {dirty && !save.isPending && <span className="text-sm text-zinc-500">Modifications non enregistrées.</span>}
      </div>
    </Card>
  )
}

const ARTIFACT_LABEL: Record<string, string> = {
  'delivery-note': 'Bon de livraison',
  'test-report': 'Rapport de tests',
  'security-report': 'Rapport de sécurité',
}

function Exports({ release }: { release: ReleaseDetail }) {
  const { data, isPending } = useExports(release.tag)
  const generate = useGenerateExport(release.tag)

  return (
    <Card
      title="Livrables"
      action={
        <span className="flex gap-2">
          <button
            type="button"
            onClick={() => generate.mutate({ kind: 'delivery-note', format: 'pdf' })}
            disabled={generate.isPending}
            className="rounded-md border border-zinc-300 bg-white px-3 py-1 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-50"
          >
            Bon de livraison (PDF)
          </button>
          <button
            type="button"
            onClick={() => generate.mutate({ kind: 'delivery-note', format: 'json' })}
            disabled={generate.isPending}
            className="rounded-md border border-zinc-300 bg-white px-3 py-1 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-50"
          >
            JSON
          </button>
        </span>
      }
    >
      {/* Dit avant tout téléchargement, parce que c'est ce qui décide si le
          document peut être transmis. La mise en page du bon de livraison est un
          élément contractuel, et elle n'a pas encore été validée par le client :
          chaque page produite porte la mention « PROVISOIRE ». */}
      <p className="mb-3 flex items-start gap-2 rounded-md border border-zinc-200 px-3 py-2 text-sm text-zinc-600">
        <Icon name="alert" className="mt-0.5 size-4 shrink-0 text-warn-700" />
        <span>
          La mise en page du bon de livraison attend la validation du client. Les documents produits portent la mention
          « PROVISOIRE » et ne doivent pas être transmis tels quels. Leur contenu, lui, est complet.
        </span>
      </p>

      {generate.isError && <p className="mb-3 text-sm text-critical-700">{(generate.error as Error).message}</p>}
      {generate.isSuccess && (
        <p className="mb-3 text-sm text-zinc-500">
          Génération mise en file. Le document apparaîtra ici une fois produit.
        </p>
      )}

      {isPending && <Skeleton className="h-16 w-full" />}

      {data &&
        (data.artifacts.length === 0 ? (
          <EmptyState>Aucun livrable produit pour cette release.</EmptyState>
        ) : (
          <ul className="flex flex-col gap-2">
            {data.artifacts.map((artifact) => (
              <li
                key={artifact.id}
                className="flex flex-wrap items-baseline gap-x-3 gap-y-1 rounded-md border border-zinc-200 px-3 py-2 text-sm"
              >
                <span className="font-medium text-zinc-900">{ARTIFACT_LABEL[artifact.kind] ?? artifact.kind}</span>
                <span className="uppercase text-zinc-500">{artifact.format}</span>
                <span className="text-zinc-500">{formatBytes(artifact.size_bytes)}</span>
                <span className="text-zinc-500">
                  {artifact.generated_by} — {formatDateTime(artifact.generated_at)}
                </span>
                <a
                  href={downloadURL(release.tag, artifact.id)}
                  className="ml-auto rounded-md border border-zinc-300 bg-white px-3 py-1 font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
                >
                  Télécharger
                </a>
              </li>
            ))}
          </ul>
        ))}
    </Card>
  )
}

function formatBytes(size: number): string {
  if (size < 1024) return `${size} o`
  if (size < 1024 * 1024) return `${Math.round(size / 1024)} Kio`
  return `${(size / (1024 * 1024)).toFixed(1)} Mio`
}
