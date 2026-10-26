import { useState } from 'react'
import { Card, EmptyState, ErrorState, Skeleton } from '../../components/primitives'
import { hasAtLeast, useSession } from '../auth/session'
import { useQueue, useSetLimit, type Queue } from './api'

/*
 * Réglage des files.
 *
 * Le plafond d'une file décide de ce que le cluster mène de front. Il vit dans le
 * catalogue, donc dans un ConfigMap : le changer demandait de reconstruire et de
 * redéployer, ce qui n'est pas un réglage mais une livraison. Les valeurs posées
 * ici vivent en base, sont relues par l'ordonnanceur à chaque tour, et prennent
 * donc effet en deux secondes.
 *
 * Le défaut du catalogue reste affiché à côté, et le retour à cette valeur reste
 * possible : un réglage qu'on ne peut pas défaire est un réglage qu'on n'ose pas
 * toucher.
 */

/** Borne du formulaire, alignée sur celle qu'applique l'API. */
const MAX_LIMIT = 20

export function QueueSettingsScreen() {
  const { data, isPending, error, refetch } = useQueue()
  const session = useSession()

  const canEdit = session.kind === 'authenticated' && hasAtLeast(session.identity.role, 'admin')

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-lg font-semibold text-zinc-900">Paramètres des files</h2>
        {data && (
          <p className="text-sm text-zinc-500">
            Plafond global&nbsp;: {data.max_concurrent} exécutions, toutes files confondues
          </p>
        )}
      </div>

      {isPending && <Skeleton className="h-40 w-full" />}
      {error && (
        <ErrorState message="Les files n'ont pas pu être chargées." onRetry={() => void refetch()} />
      )}

      {data && (
        <Card>
          {!canEdit && (
            <p className="mb-3 text-xs text-zinc-500">
              Réglage réservé à l'administration. Relever un plafond engage le cluster, le baisser
              rallonge toute vérification.
            </p>
          )}

          {data.queues.length === 0 ? (
            <EmptyState>Aucune file déclarée.</EmptyState>
          ) : (
            <ul className="flex flex-col gap-2">
              {data.queues.map((queue) => (
                <LimitRow key={queue.kind} queue={queue} canEdit={canEdit} />
              ))}
            </ul>
          )}
        </Card>
      )}

      <p className="text-xs text-zinc-500">
        Le plafond global prime sur ceux des files et n'est pas leur somme : il protège le cluster,
        et se règle dans le catalogue.
      </p>
    </div>
  )
}

function LimitRow({ queue, canEdit }: { queue: Queue; canEdit: boolean }) {
  // La valeur saisie est un état local, initialisé sur la valeur effective. Un
  // champ piloté directement par la requête se réinitialiserait toutes les deux
  // secondes, sous les doigts de qui le remplit.
  const [draft, setDraft] = useState<string | null>(null)
  const setLimit = useSetLimit()

  const value = draft ?? String(queue.max_concurrent)
  const parsed = Number.parseInt(value, 10)
  const valid = Number.isFinite(parsed) && parsed >= 1 && parsed <= MAX_LIMIT
  const changed = valid && parsed !== queue.max_concurrent

  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md border border-zinc-200 px-3 py-2">
      <span className="font-medium text-zinc-900">{queue.label}</span>
      <span className="font-mono text-xs text-zinc-400">{queue.kind}</span>

      {!queue.known && (
        <span className="rounded border border-zinc-200 px-1.5 py-0.5 text-xs text-zinc-500">
          tâche interne
        </span>
      )}

      {queue.max_concurrent === 1 && (
        // Le cas qui mérite d'être expliqué : une file à une place n'est pas un
        // réglage frileux mais une contrainte du pipeline lui-même.
        <span className="text-xs text-zinc-500">une seule à la fois</span>
      )}

      <label className="ml-auto flex items-center gap-2 text-xs text-zinc-600">
        <span>Maximum en parallèle</span>
        <input
          type="number"
          min={1}
          max={MAX_LIMIT}
          value={value}
          disabled={!canEdit || setLimit.isPending}
          onChange={(event) => setDraft(event.target.value)}
          className="w-20 rounded-md border border-zinc-200 bg-white px-2 py-1 text-sm tabular-nums text-zinc-900 disabled:opacity-50"
        />
      </label>

      <button
        type="button"
        disabled={!canEdit || !changed || setLimit.isPending}
        onClick={() =>
          setLimit.mutate(
            { kind: queue.kind, max: parsed },
            // Le brouillon est relâché après coup : la valeur affichée redevient
            // celle du serveur, seule à faire foi.
            { onSuccess: () => setDraft(null) },
          )
        }
        className="rounded-md bg-zinc-900 px-3 py-1.5 text-xs font-medium text-white transition-colors hover:bg-zinc-700 disabled:opacity-40"
      >
        Enregistrer
      </button>

      {queue.overridden && (
        <button
          type="button"
          disabled={!canEdit || setLimit.isPending}
          onClick={() =>
            setLimit.mutate({ kind: queue.kind, max: null }, { onSuccess: () => setDraft(null) })
          }
          title={`Revenir au plafond du catalogue (${queue.default_max_concurrent})`}
          className="rounded-md border border-zinc-200 px-2 py-1.5 text-xs font-medium text-zinc-600 transition-colors hover:bg-zinc-100 disabled:opacity-40"
        >
          Défaut ({queue.default_max_concurrent})
        </button>
      )}

      {!valid && (
        <p className="w-full text-xs text-critical-700">
          Attendu entre 1 et {MAX_LIMIT}. Un plafond nul immobiliserait la file.
        </p>
      )}

      {setLimit.isError && (
        <p className="w-full text-xs text-critical-700">Le réglage a été refusé.</p>
      )}
    </li>
  )
}
