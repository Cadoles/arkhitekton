import { useState } from 'react'
import { Link } from 'react-router'
import { Icon } from '../../components/Icon'
import { Card, EmptyState, ErrorState, Skeleton } from '../../components/primitives'
import { formatDateTime } from '../../lib/format'
import {
  trackerName,
  useDeclareVersion,
  useVersions,
  type Completion,
  type SyncState,
  type TrackerRef,
  type Version,
} from './api'

/*
 * Liste des versions d'un projet.
 *
 * Deux origines, et l'écran doit les distinguer. Un projet **suivi** tire ses
 * versions de son outil, qui en fait autorité : l'âge des données est affiché
 * avec elles, parce qu'un service tiers peut ne pas répondre et qu'un écran qui
 * montre des chiffres sans dire de quand ils datent est trompeur (doc 08 §8.2).
 *
 * Un projet **sans suivi** les déclare lui-même. Rien ne les confirmera, rien
 * n'est à attendre, et c'est ici qu'on les crée — sans ce bouton, son écran
 * restait vide sans qu'aucune issue n'y mène : ses versions naissaient à la
 * création d'une release, dont le formulaire vit sur l'écran d'une version qui
 * n'existait pas.
 */
export function VersionsScreen() {
  const { data, isPending, error, refetch } = useVersions()

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-2xl font-semibold text-zinc-900">Versions</h1>
        {data && !data.sync.tracked && <DeclareVersionButton />}
      </div>

      {isPending && <Skeleton className="h-64 w-full" />}
      {error && <ErrorState message="Les versions n'ont pas pu être chargées." onRetry={() => void refetch()} />}

      {data && (
        <>
          <SyncBanner sync={data.sync} />

          <VersionLists versions={data.versions} sync={data.sync} />
        </>
      )}
    </div>
  )
}

/**
 * Versions actives d'abord, historique replié.
 *
 * Seules les versions non closes comptent pour le pilotage. Les seize closes du projet
 * viennent d'un ancien système de versionnement — `MSE3-1.5`, `MSE 3.1.0` — et les
 * mêler aux quatre actives noyait ces dernières dans une liste où rien ne distinguait
 * l'historique du courant.
 *
 * Elles ne sont pas masquées pour autant : une release peut s'y rattacher, et cacher
 * une version que Redmine déclare ferait douter de l'exhaustivité de l'écran.
 *
 * Le critère est « non close » et non « ouverte » : une version verrouillée n'accepte
 * plus de demande mais n'est pas terminée.
 */
function VersionLists({ versions, sync }: { versions: Version[]; sync: SyncState }) {
  const [showClosed, setShowClosed] = useState(false)

  const active = versions.filter((version) => version.status !== 'closed')
  const closed = versions.filter((version) => version.status === 'closed')

  if (versions.length === 0) {
    return (
      <Card title="Versions">
        <EmptyState>
          {!sync.tracked
            ? // Un projet sans suivi tire ses versions de ses tags : il n'y a
              // aucune synchronisation à attendre, et le dire évite de chercher
              // une panne qui n'existe pas.
              'Ce projet ne déclare aucun outil de suivi. Ses versions apparaîtront avec ses premières releases.'
            : sync.ever_ran
              ? 'Aucune version déclarée dans le suivi de ce projet.'
              : "La synchronisation du suivi n'a pas encore tourné."}
        </EmptyState>
      </Card>
    )
  }

  return (
    <>
      {active.length === 0 ? (
        <Card title="Versions en cours">
          <EmptyState>
            Aucune version ouverte. Les {closed.length} versions closes sont dans l'historique
            ci-dessous.
          </EmptyState>
        </Card>
      ) : (
        <ul className="flex flex-col gap-2">
          {active.map((version) => (
            <VersionRow key={version.id} version={version} tracker={sync.tracker} />
          ))}
        </ul>
      )}

      {closed.length > 0 && (
        <div>
          <button
            type="button"
            onClick={() => setShowClosed((value) => !value)}
            aria-expanded={showClosed}
            className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
          >
            {showClosed ? 'Masquer' : 'Afficher'} les {closed.length} versions closes
          </button>

          {showClosed && (
            <ul className="mt-2 flex flex-col gap-2">
              {closed.map((version) => (
                <VersionRow key={version.id} version={version} tracker={sync.tracker} />
              ))}
            </ul>
          )}
        </div>
      )}
    </>
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
  // Rien à dire d'une synchronisation qui n'existe pas : un projet sans suivi
  // n'attend aucune passe, et un bandeau permanent serait un reproche.
  if (!sync.tracked) return null

  if (!sync.ever_ran) {
    return (
      <p className="flex items-start gap-2 rounded-md border border-zinc-200 bg-zinc-50 px-3 py-2 text-sm text-zinc-600">
        <Icon name="clock" className="mt-0.5 size-4 shrink-0" />
        La synchronisation du suivi n'a jamais tourné. Les versions apparaîtront à sa première passe.
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
          n'intègrent pas les modifications faites depuis dans {trackerName(sync.tracker)}.
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

function VersionRow({ version, tracker }: { version: Version; tracker?: TrackerRef }) {
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
          // Le nom vient d'un tag de release, pas du suivi : la synchronisation
          // n'a trouvé aucune version correspondante.
          <span className="flex items-center gap-1 text-sm text-warn-700">
            <Icon name="alert" className="size-3.5" />
            inconnue de {trackerName(tracker)}
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

/**
 * Déclarer une version, sur un projet qui n'a pas d'outil de suivi.
 *
 * N'apparaît que là : ailleurs, les versions viennent de l'outil, et le serveur
 * refuse la déclaration manuelle. Proposer le bouton partout ferait cliquer vers
 * un refus dont la raison est structurelle.
 */
function DeclareVersionButton() {
  const declare = useDeclareVersion()
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')

  const submit = (event: React.FormEvent) => {
    event.preventDefault()
    const trimmed = name.trim()
    if (trimmed === '') return
    declare.mutate(trimmed, {
      onSuccess: () => {
        setName('')
        setOpen(false)
      },
    })
  }

  if (!open) {
    return (
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="rounded-md border border-zinc-900 bg-zinc-900 px-3 py-1.5 text-sm font-medium text-white transition-colors hover:bg-zinc-700"
      >
        Déclarer une version
      </button>
    )
  }

  return (
    <form onSubmit={submit} className="flex flex-wrap items-center gap-2">
      <input
        autoFocus
        value={name}
        onChange={(event) => setName(event.target.value)}
        placeholder="1.8.0"
        aria-label="Numéro de la version"
        className="w-40 rounded-md border border-zinc-300 bg-white px-2 py-1.5 font-mono text-sm text-zinc-900"
      />
      <button
        type="submit"
        disabled={declare.isPending || name.trim() === ''}
        className="rounded-md border border-zinc-900 bg-zinc-900 px-3 py-1.5 text-sm font-medium text-white transition-colors hover:bg-zinc-700 disabled:opacity-40"
      >
        Déclarer
      </button>
      <button
        type="button"
        onClick={() => {
          setOpen(false)
          setName('')
        }}
        className="rounded-md border border-zinc-300 bg-white px-3 py-1.5 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
      >
        Annuler
      </button>
      {declare.isError && (
        <p className="w-full text-sm text-critical-700">{(declare.error as Error).message}</p>
      )}
    </form>
  )
}
