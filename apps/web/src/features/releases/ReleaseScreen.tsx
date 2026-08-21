import { useState } from 'react'
import { Link, useParams } from 'react-router'
import { TestsPanel } from './TestsPanel'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Icon } from '../../components/Icon'
import { CollapsibleSection } from '../../components/CollapsibleSection'
import { ReleaseBadges } from './ReleaseBadges'
import { ReleaseNotes } from './ReleaseNotes'
import { Card, EmptyState, ErrorState, Skeleton, StatusBadge } from '../../components/primitives'
import { formatDateTime } from '../../lib/format'
import { status } from '../../lib/status'
import { useEnvironments } from '../environments/api'
import { hasAtLeast, useSession } from '../auth/session'
import {
  downloadURL,
  useExports,
  useGenerateExport,
  useRebuild,
  useRelease,
  useReleaseRelease,
  usePromote,
  type Image,
  type PromotionTarget,
  type ReleaseDetail,
  type ReleaseJob,
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
    <div className="flex flex-col gap-5">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <Header tag={tag ?? ''} release={data} />
        <Link
          to="/releases"
          className="flex items-center gap-1.5 rounded-md border border-zinc-300 bg-white px-2.5 py-1.5 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
        >
          <Icon name="arrow-left" className="size-4" />
          Toutes les releases
        </Link>
      </div>

      {isPending && <Skeleton className="h-64 w-full" />}
      {error && <ErrorState message="Cette release n'a pas pu être chargée." onRetry={() => void refetch()} />}

      {data && (
        <>
          {/* Les deux actions restent en tête et hors des sections repliables :
              elles ne s'affichent que lorsqu'une décision est attendue, et une
              décision qu'il faut déplier n'est pas demandée. */}
          <RebuildAction release={data} />
          <ReleaseAction release={data} />

          {/* Le score en premier : c'est le bloc qu'on lit d'un coup d'œil, et
              celui qui dit s'il faut ouvrir le reste. */}
          <TestsPanel tag={data.tag} jobs={data.jobs ?? []} />

          {/* L'état technique d'abord — quelles images existent, où elles
              tournent — puis le contenu fonctionnel. On vient sur cet écran pour
              savoir où en est la release ; ce qu'elle apporte se relit ensuite,
              au moment de rédiger ce qui part chez le client. */}
          <Images release={data} />
          <Deployment release={data} />

          <ReleaseNotes release={data} />
          <Exports release={data} />

          {/* Les exécutions en dernier et repliées : c'est de la matière de
              diagnostic, consultée quand quelque chose ne va pas. Le reste de
              l'écran répond déjà à « où en est cette release », et une liste de
              runs entre les deux repoussait les exports hors du premier écran. */}
          <Executions release={data} />
        </>
      )}
    </div>
  )
}

/**
 * En-tête de la release — repris de la maquette.
 *
 * Le tag, son état en badge, la nature en dessous, puis une ligne de repères en
 * chiffres : version d'image, commit, date de création. Le tag reste en
 * chasse fixe, contrairement à la maquette : c'est un identifiant qu'on compare
 * caractère par caractère avec un tag Git et un tag d'image (B17).
 */
function Header({ tag, release }: { tag: string; release?: ReleaseDetail }) {
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="font-mono text-2xl font-semibold text-zinc-900">{release?.tag ?? tag}</h1>
        {release && (
          <>
            <StatusBadge style={status.release(release.state)} />
            <ReleaseBadges release={release} />
          </>
        )}
      </div>

      {release && (
        <>
          <p className="text-zinc-600">{KIND_LABEL[release.kind] ?? release.kind}</p>

          {/* Les repères d'identité, en une ligne. Ils occupaient une carte
              entière plus bas, qui redisait le tag, l'état et la nature déjà
              portés ici — et reléguait sous le score ce qui identifie la chose
              qu'on est en train de lire. */}
          <div className="flex flex-wrap items-center gap-x-4 gap-y-1 font-mono text-sm text-zinc-500">
            {/* La version d'image n'est rappelée que lorsqu'elle diffère du tag,
                c'est-à-dire sur une finale produite par re-tag. La montrer partout
                noierait le seul cas où elle surprend. */}
            {release.image_version !== release.tag && (
              <span className="text-zinc-700">image {release.image_version}</span>
            )}

            {/* Le commit avant la référence : c'est lui qui identifie ce qui
                tourne. La référence n'est qu'un point de départ, et elle a pu
                bouger depuis. */}
            {release.git_commit && (
              <span className="inline-flex items-center gap-1" title={release.git_commit}>
                <Icon name="commit" className="size-3.5" />
                {release.git_commit.slice(0, 12)}
              </span>
            )}
            {release.git_ref && (
              <span className="inline-flex items-center gap-1">
                <Icon name="branch" className="size-3.5" />
                {release.git_ref}
              </span>
            )}
            {release.source_tag && (
              <span className="inline-flex items-center gap-1">
                <Icon name="tag" className="size-3.5" />
                re-tag de {release.source_tag}
              </span>
            )}

            <span className="inline-flex items-center gap-1 font-sans">
              <Icon name="user" className="size-3.5" />
              {release.created_by}
            </span>
            <span className="inline-flex items-center gap-1">
              <Icon name="clock" className="size-3.5" />
              créée le {formatDateTime(release.created_at)}
            </span>
          </div>

          {release.git_ref && !release.git_commit && (
            // Une release qui ne désigne qu'une branche n'est pas reproductible :
            // la branche bouge, et reconstruire produirait une autre image.
            <p className="flex items-start gap-2 text-sm text-warn-700">
              <Icon name="alert" className="mt-0.5 size-4 shrink-0" />
              Référence non résolue en commit : cette release n&apos;est pas reproductible.
            </p>
          )}

          {release.image_version !== release.tag && (
            // Le point le plus contre-intuitif de tout le dispositif : ce qui part
            // en production porte encore la version de la RC. Le taire ferait
            // conclure à une erreur de déploiement en lisant l'écran des
            // environnements.
            <p className="flex max-w-3xl items-start gap-2 rounded-md border border-zinc-200 px-3 py-2 text-sm text-zinc-600">
              <Icon name="alert" className="mt-0.5 size-4 shrink-0 text-warn-700" />
              <span>
                Re-tag de <span className="font-mono">{release.image_version}</span> : l&apos;image
                est bit pour bit celle qui a été validée, et son{' '}
                <span className="font-mono">version.txt</span> porte donc encore la version de la
                pré-release. C&apos;est cette valeur, et non le tag, que rapportera le portail.
              </span>
            </p>
          )}
        </>
      )}
    </div>
  )
}

const KIND_LABEL: Record<string, string> = {
  pre: 'Pré-release',
  final: 'Release finale',
  patch: 'Correctif',
}

function Images({ release }: { release: ReleaseDetail }) {
  const images = release.images ?? []
  // Le digest de référence est celui de l'image construite, en tête de liste.
  const reference = images[0]?.digest

  return (
    <CollapsibleSection title="Images" count={images.length}>
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
    </CollapsibleSection>
  )
}

function ImageRow({ image, reference }: { image: Image; reference?: string }) {
  // Un digest différent de celui de l'image construite signale une image
  // reconstruite entre-temps : ce qui est déployé n'est plus ce qui a été validé.
  const diverged = Boolean(reference && image.digest && image.digest !== reference)

  return (
    <li className="flex flex-wrap items-baseline gap-x-3 gap-y-1 rounded-md border border-zinc-200 px-3 py-2 text-sm">
      {/* La référence complète : c'est ce qu'on copie pour tirer l'image ou
          l'inspecter. Le nom court seul ne désigne rien — deux projets du même
          registre portent un `portal` — et obligeait à reconstituer le préfixe
          de tête. */}
      <span className="font-mono text-zinc-900">
        {image.reference ?? image.repository}:{image.tag}
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
    <CollapsibleSection title="Déploiement" count={(release.targets ?? []).length}>
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
    </CollapsibleSection>
  )
}

function Executions({ release }: { release: ReleaseDetail }) {
  const jobs = release.jobs ?? []

  return (
    <CollapsibleSection title="Exécutions" count={jobs.length} defaultOpen={false}>
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
    </CollapsibleSection>
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


/**
 * Validation d'une release : la décision humaine du cycle (doc 04 §4.2).
 *
 * N'apparaît que sur une release `ready` — celle dont les tests ont conclu et qui
 * attend qu'on tranche. Ailleurs elle n'aurait pas de sens : rien à valider avant
 * les tests, plus rien à décider après.
 *
 * Ressaisie du tag exigée : valider autorise la promotion vers les environnements
 * et le re-tag en release finale. C'est le geste qui engage ce qui partira chez
 * l'utilisateur (doc 08 §8.5).
 */
function ReleaseAction({ release }: { release: ReleaseDetail }) {
  const session = useSession()
  const validate = useReleaseRelease(release.tag)
  const [confirming, setConfirming] = useState(false)

  const canRelease =
    session.kind === 'authenticated' && hasAtLeast(session.identity.role, 'releaser')

  if (release.state !== 'ready' || !canRelease) return null

  return (
    <Card title="Décision">
      <p className="text-sm text-zinc-600">
        Les tests de cette release ont conclu. La valider l'autorise à partir en environnement
        et permet d'en tirer une release finale par re-tag.
      </p>

      {validate.isError && (
        <p className="mt-2 flex items-start gap-2 text-sm text-critical-700">
          <Icon name="alert" className="mt-0.5 size-4 shrink-0" />
          {(validate.error as Error).message}
        </p>
      )}

      <button
        type="button"
        onClick={() => setConfirming(true)}
        disabled={validate.isPending}
        className="mt-3 rounded-md border border-zinc-900 bg-zinc-900 px-3 py-1 text-sm font-medium text-white transition-colors hover:bg-zinc-700 disabled:opacity-40"
      >
        Valider la release
      </button>

      {confirming && (
        <ConfirmDialog
          title={`Valider la release ${release.tag}`}
          confirmLabel="Valider"
          challenge={release.tag}
          onConfirm={() => {
            setConfirming(false)
            validate.mutate()
          }}
          onCancel={() => setConfirming(false)}
          pending={validate.isPending}
        >
          <p>
            <span className="font-mono">{release.tag}</span> deviendra promouvable sur les
            environnements, et pourra être re-taguée en release finale. Cette décision est
            consignée au journal d'audit.
          </p>
        </ConfirmDialog>
      )}
    </Card>
  )
}


/**
 * Les natures de job qui vérifient une release, par opposition à celle qui la
 * construit. Sert à dire **ce qui** a échoué : l'état `failed` d'une release ne
 * le dit pas, il couvre les deux.
 */
const VERIFICATION_KINDS = ['static-tests', 'security-audit', 'integration-tests', 'e2e-tests']

/**
 * Nomme l'étape à laquelle la release a échoué.
 *
 * Une release `failed` peut l'être parce que sa construction a échoué, ou parce
 * que ses vérifications ont rendu un verdict négatif sur une image parfaitement
 * construite. Annoncer « construction en échec » dans le second cas envoie
 * chercher la cause au mauvais endroit — constaté sur une release dont le build
 * avait réussi et dont seul l'audit de sécurité avait échoué.
 *
 * On lit la **dernière** construction : les précédentes ont pu échouer avant une
 * relance réussie, et c'est la dernière qui décrit l'image en place.
 */
function failedStage(jobs: ReleaseJob[]): 'build' | 'verification' | 'inconnue' {
  const builds = jobs.filter((job) => job.kind === 'build')
  const lastBuild = builds.length > 0 ? builds[builds.length - 1] : undefined

  if (lastBuild && lastBuild.state !== 'succeeded') return 'build'
  if (jobs.some((job) => VERIFICATION_KINDS.includes(job.kind) && job.state === 'failed'))
    return 'verification'
  return lastBuild ? 'inconnue' : 'build'
}

/**
 * Relance de la construction, après un échec.
 *
 * N'apparaît que sur une release en échec : ailleurs, reconstruire écraserait une
 * image que quelqu'un est peut-être en train de tester. Une release finale n'est
 * jamais concernée — c'est un re-tag, son image est celle de sa RC source.
 */
function RebuildAction({ release }: { release: ReleaseDetail }) {
  const session = useSession()
  const rebuild = useRebuild(release.tag)

  const canBuild =
    session.kind === 'authenticated' && hasAtLeast(session.identity.role, 'operator')

  if (release.state !== 'failed' || release.kind === 'final' || !canBuild) return null

  const stage = failedStage(release.jobs ?? [])
  const title =
    stage === 'build'
      ? 'Construction en échec'
      : stage === 'verification'
        ? 'Vérifications en échec'
        : 'Release en échec'

  return (
    <Card title={title}>
      {stage === 'verification' ? (
        <p className="text-sm text-zinc-600">
          Les images ont été construites ; ce sont les vérifications qui ont échoué. La cause est
          dans leurs journaux, listés plus bas. Relancer reconstruit tout depuis{' '}
          <span className="font-mono">{release.git_ref || 'la référence enregistrée'}</span> puis
          rejoue les vérifications — inutile si le verdict portait sur le code, qu&apos;il faut
          alors corriger d&apos;abord.
        </p>
      ) : (
        <p className="text-sm text-zinc-600">
          Relancer reconstruit depuis{' '}
          <span className="font-mono">{release.git_ref || 'la référence enregistrée'}</span>, sans
          créer de nouvelle version. À faire quand la cause était extérieure au code — droits
          d&apos;accès, registre injoignable, nœud saturé.
        </p>
      )}

      {rebuild.isError && (
        <p className="mt-2 flex items-start gap-2 text-sm text-critical-700">
          <Icon name="alert" className="mt-0.5 size-4 shrink-0" />
          {(rebuild.error as Error).message}
        </p>
      )}

      {/* Pas de ressaisie : rien ne part vers l'extérieur qui ne puisse être
          refait, et un échec de build se relance souvent plusieurs fois de
          suite. Exiger une confirmation à chaque essai serait du bruit. */}
      <button
        type="button"
        onClick={() => rebuild.mutate()}
        disabled={rebuild.isPending}
        className="mt-3 rounded-md border border-zinc-300 bg-white px-3 py-1 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-40"
      >
        {rebuild.isPending ? 'Relance…' : 'Relancer la construction'}
      </button>
    </Card>
  )
}
