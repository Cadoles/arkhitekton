import { Link, useParams } from 'react-router'
import { Icon } from '../../components/Icon'
import { Card, EmptyState, ErrorState, Skeleton } from '../../components/primitives'
import { formatDateTime } from '../../lib/format'
import { SignoffPanel } from '../releases/SignoffPanel'
import { TestsPanel } from '../releases/TestsPanel'
import {
  describe,
  promotionSuffix,
  useEnvironment,
  type DeployedRelease,
  type EnvironmentDetail,
  type FlowStep,
} from './api'

/*
 * Vue détaillée d'un environnement.
 *
 * L'écran est une **jointure**, et c'est ce qui le rend délicat : l'environnement
 * porte une version, la release porte le contenu. Le rapprochement se fait sur la
 * version de l'image et jamais sur le tag — ce que le portail rapporte est la
 * version bakée au build, restée celle de la RC pour toute release finale produite
 * par re-tag (B8, B17).
 *
 * Trois issues à ce rapprochement, et les trois s'affichent différemment : la
 * release est identifiée ; plusieurs candidates partagent l'image et rien ne
 * tranche ; aucune release connue ne correspond. La troisième est banale — le
 * dashboard ne connaît que ce qu'il a produit.
 */
export function EnvironmentScreen() {
  const { env } = useParams()
  const { data, isPending, error, refetch } = useEnvironment(env)

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-baseline justify-between gap-3">
        <div className="flex flex-wrap items-baseline gap-3">
          <h1 className="text-2xl font-semibold text-zinc-900">{data?.label ?? env}</h1>
          <Link to="/environments" className="text-sm text-zinc-500 hover:underline">
            Tous les environnements
          </Link>
        </div>
        {env && (
          <Link
            to={`/environments/${encodeURIComponent(env)}/history`}
            className="text-sm text-zinc-500 hover:underline"
          >
            Historique
          </Link>
        )}
      </div>

      {isPending && <Skeleton className="h-64 w-full" />}
      {error && <ErrorState message="L'environnement n'a pas pu être chargé." onRetry={() => void refetch()} />}

      {data && (
        <>
          <PromotionFlow flow={data.flow} />
          <StateCard environment={data} />
          <DeployedCard environment={data} />
          {/* Le score, les résultats et la validation sont ceux de la release
              déployée : c'est la jointure qui donne son intérêt à cet écran. La
              validation est restreinte à cet environnement — une release validée
              en qualif ne l'est pas en préproduction. */}
          {data.deployed && (
            <>
              <TestsPanel tag={data.deployed.tag} />
              {env && <SignoffPanel tag={data.deployed.tag} environment={env} />}
            </>
          )}
        </>
      )}
    </div>
  )
}

// PromotionFlow montre le chemin qu'une version parcourt, et où l'on se trouve.
function PromotionFlow({ flow }: { flow: FlowStep[] }) {
  if (flow.length === 0) return null

  return (
    <Card title="Flux de promotion">
      <ol className="flex flex-wrap items-center gap-1">
        {flow.map((step, index) => (
          <li key={step.id} className="flex items-center gap-1">
            {index > 0 && <Icon name="arrow-right" className="size-4 shrink-0 text-zinc-300" />}
            <Link
              to={`/environments/${encodeURIComponent(step.id)}`}
              aria-current={step.current ? 'page' : undefined}
              className={`rounded-md border px-3 py-1.5 text-sm transition-colors ${
                step.current
                  ? 'border-zinc-900 bg-zinc-100 font-medium text-zinc-900'
                  : 'border-zinc-200 text-zinc-600 hover:bg-zinc-100'
              }`}
            >
              <span className="block">{step.label}</span>
              {/* La version de chaque étape : c'est ce qui rend le fil utile —
                  voir d'un coup jusqu'où une version est allée. */}
              <span className="block font-mono text-xs text-zinc-500">{step.version || '—'}</span>
            </Link>
          </li>
        ))}
      </ol>
    </Card>
  )
}

const TONE = {
  ok: { text: 'text-healthy-700', icon: 'check' },
  warn: { text: 'text-warn-700', icon: 'clock' },
  neutral: { text: 'text-zinc-500', icon: 'minus' },
} as const

function StateCard({ environment }: { environment: EnvironmentDetail }) {
  const suffix = promotionSuffix(environment)
  const summary = describe(environment)
  const tone = TONE[summary.tone]

  return (
    <Card
      title="État constaté"
      action={
        suffix && (
          <span className="rounded bg-zinc-100 px-1.5 py-0.5 font-mono text-xs text-zinc-500">
            {suffix}
          </span>
        )
      }
    >
      <p className={`flex items-start gap-2 text-sm ${tone.text}`}>
        <Icon name={tone.icon} className="mt-0.5 size-4 shrink-0" />
        {summary.text}
      </p>

      <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
        {environment.portal_version && (
          <>
            <dt className="text-zinc-500">Version constatée</dt>
            <dd className="font-mono text-zinc-900">{environment.portal_version}</dd>
          </>
        )}
        {environment.migration_state && (
          <>
            <dt className="text-zinc-500">Migrations</dt>
            <dd className="text-zinc-900">{environment.migration_state}</dd>
          </>
        )}
        {environment.declared_state && (
          <>
            <dt className="text-zinc-500">Déployeur</dt>
            <dd className="text-zinc-900">{environment.declared_state}</dd>
          </>
        )}
        <dt className="text-zinc-500">Namespace</dt>
        <dd className="font-mono text-zinc-900">{environment.namespace}</dd>
        {environment.observed_at && (
          <>
            <dt className="text-zinc-500">Constaté</dt>
            <dd className="text-zinc-600">{formatDateTime(environment.observed_at)}</dd>
          </>
        )}
      </dl>

      {environment.last_promotion && (
        <p className="mt-3 border-t border-zinc-200 pt-3 text-sm text-zinc-600">
          Dernière promotion demandée : <span className="font-mono">{environment.last_promotion.tag}</span> —{' '}
          {formatDateTime(environment.last_promotion.pushed_at)}
        </p>
      )}
    </Card>
  )
}

function DeployedCard({ environment }: { environment: EnvironmentDetail }) {
  const deployed = environment.deployed

  if (!deployed) {
    return (
      <Card title="Release déployée">
        {environment.portal_version ? (
          // Cas banal : l'application est déployée depuis des années, et le
          // dashboard ne connaît que les releases créées par lui.
          <EmptyState>
            La version <span className="font-mono">{environment.portal_version}</span> ne correspond à aucune release
            connue du dashboard. Elle a été déployée avant lui, ou par un autre chemin.
          </EmptyState>
        ) : (
          <EmptyState>
            {environment.observable
              ? "Aucune version constatée : rien à rapprocher."
              : "Cet environnement n'est pas observable depuis ici."}
          </EmptyState>
        )}
      </Card>
    )
  }

  return (
    <Card title="Release déployée">
      {deployed.ambiguous && (
        // Le re-tag rend ce cas normal : la RC et la finale partagent la même
        // image. Le taire ferait afficher l'une pour l'autre.
        <p className="mb-3 flex items-start gap-2 rounded-md border border-warn-500 bg-warn-50 px-3 py-2 text-sm text-warn-700">
          <Icon name="alert" className="mt-0.5 size-4 shrink-0" />
          <span>
            Plusieurs releases portent l'image <span className="font-mono">{deployed.image_version}</span> et aucune
            promotion enregistrée ne permet de trancher. Celle affichée est la plus récente : ce n'est pas
            nécessairement celle qui tourne.
          </span>
        </p>
      )}

      <DeployedFields deployed={deployed} />
    </Card>
  )
}

function DeployedFields({ deployed }: { deployed: DeployedRelease }) {
  return (
    <>
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
        <dt className="text-zinc-500">Release</dt>
        <dd>
          <Link
            to={`/releases/${encodeURIComponent(deployed.tag)}`}
            className="font-mono font-medium text-zinc-900 hover:underline"
          >
            {deployed.tag}
          </Link>
        </dd>
        <dt className="text-zinc-500">Version de l'image</dt>
        <dd className="font-mono text-zinc-900">{deployed.image_version}</dd>
        <dt className="text-zinc-500">Nature</dt>
        <dd className="text-zinc-900">{deployed.kind}</dd>
        {deployed.git_ref && (
          <>
            <dt className="text-zinc-500">Référence Git</dt>
            <dd className="font-mono text-zinc-900">{deployed.git_ref}</dd>
          </>
        )}
        {deployed.promoted_at ? (
          <>
            <dt className="text-zinc-500">Promue ici</dt>
            <dd className="text-zinc-900">
              <span className="font-mono">{deployed.promoted_tag}</span> — {formatDateTime(deployed.promoted_at)}
            </dd>
          </>
        ) : (
          <>
            <dt className="text-zinc-500">Promue ici</dt>
            {/* Le rapprochement s'est fait sur l'image seule : le dashboard n'a
                jamais promu cette release ici. */}
            <dd className="text-zinc-500">jamais, par le dashboard</dd>
          </>
        )}
      </dl>

      {deployed.image_version !== deployed.tag && (
        // Le point le plus contre-intuitif : ce qui tourne porte la version de la
        // RC. Sans cette phrase, l'écart entre le tag affiché et la version
        // constatée se lit comme une erreur de déploiement.
        <p className="mt-3 flex items-start gap-2 rounded-md border border-zinc-200 px-3 py-2 text-sm text-zinc-600">
          <Icon name="alert" className="mt-0.5 size-4 shrink-0 text-warn-700" />
          <span>
            Cette release est un re-tag de <span className="font-mono">{deployed.image_version}</span> : l'image est
            bit pour bit celle qui a été validée, et son <span className="font-mono">version.txt</span> porte donc
            encore la version de la pré-release. C'est pourquoi la version constatée diffère du tag.
          </span>
        </p>
      )}
    </>
  )
}
