import { useState } from 'react'
import { CollapsibleSection } from '../../components/CollapsibleSection'
import { Icon } from '../../components/Icon'
import { Modal } from '../../components/Modal'
import { EmptyState, Skeleton } from '../../components/primitives'
import { hasAtLeast, useSession } from '../auth/session'
import { formatDateTime } from '../../lib/format'
import {
  useAddNote,
  useNotes,
  useRemoveNote,
  useSaveManualActions,
  type NoteEntry,
  type ReleaseDetail,
} from './api'

/*
 * Note de release — reprise de la maquette, qui la sépare en « Nouveautés » et
 * « Corrections ».
 *
 * La séparation vient du **tracker** de la demande, pas de son statut : c'est la
 * nature de ce qui a été fait qui range la ligne. Une anomalie corrigée reste une
 * correction quel que soit son avancement.
 *
 * Le contenu est proposé à la création de la release — demandes en revue citées
 * par une pull request fusionnée — puis ajusté à la main. La détection ne se
 * rejoue pas : elle écraserait ces ajustements, et une note est un document qu'on
 * relit avant de l'envoyer au client.
 */
export function ReleaseNotes({ release }: { release: ReleaseDetail }) {
  const tag = release.tag
  const { data, isPending } = useNotes(tag)
  const session = useSession()

  const canEdit = session.kind === 'authenticated' && hasAtLeast(session.identity.role, 'operator')

  if (isPending) return <Skeleton className="h-32 w-full" />

  const notes = data?.notes ?? []
  const features = notes.filter((entry) => !entry.anomaly)
  const fixes = notes.filter((entry) => entry.anomaly)

  return (
    <CollapsibleSection
      title="Notes de release"
      count={notes.length}
      action={
        canEdit ? (
          <AddNote
            tag={tag}
            available={data?.available ?? []}
            error={data?.available_error}
          />
        ) : undefined
      }
    >
      <div className="flex flex-col gap-4">
        {notes.length === 0 ? (
          <EmptyState>
            Aucune demande rattachée. La détection retient les demandes en revue citées par une
            pull request fusionnée&nbsp;; les autres se choisissent dans la liste de la version.
          </EmptyState>
        ) : (
          <>
            <NoteGroup title="Nouveautés" entries={features} tag={tag} canEdit={canEdit} />
            <NoteGroup title="Corrections" entries={fixes} tag={tag} canEdit={canEdit} />
          </>
        )}

        {/* Les actions manuelles font partie de la note, pas d'une section à
            elles : ce qu'on envoie au client, c'est ce qui change, ce qui est
            corrigé, et ce qu'il doit faire à la main pour que la livraison
            prenne. Les séparer laissait la troisième derrière les exécutions,
            où personne ne la relisait avant d'éditer le bon de livraison. */}
        <ManualActions release={release} canEdit={canEdit} />
      </div>
    </CollapsibleSection>
  )
}

/**
 * Un groupe, affiché seulement s'il a du contenu.
 *
 * Une release peut n'apporter que des correctifs, et un « Nouveautés » vide
 * n'apprendrait rien tout en suggérant qu'il manque quelque chose.
 */
function NoteGroup({
  title,
  entries,
  tag,
  canEdit,
}: {
  title: string
  entries: NoteEntry[]
  tag: string
  canEdit: boolean
}) {
  if (entries.length === 0) return null

  return (
    <section>
      <h3 className="mb-2 text-sm font-semibold uppercase tracking-wide text-zinc-500">
        {title} ({entries.length})
      </h3>
      <ul className="flex flex-col">
        {entries.map((entry) => (
          <NoteRow key={entry.external_id} entry={entry} tag={tag} canEdit={canEdit} />
        ))}
      </ul>
    </section>
  )
}

function NoteRow({ entry, tag, canEdit }: { entry: NoteEntry; tag: string; canEdit: boolean }) {
  const remove = useRemoveNote(tag)

  return (
    <li className="flex flex-wrap items-baseline gap-x-3 gap-y-1 border-t border-zinc-200 py-1.5 text-sm first:border-t-0">
      {entry.url ? (
        <a
          href={entry.url}
          target="_blank"
          rel="noreferrer noopener"
          className="font-mono text-xs text-zinc-500 hover:underline"
        >
          {entry.external_id}
        </a>
      ) : (
        <span className="font-mono text-xs text-zinc-500">{entry.external_id}</span>
      )}

      {entry.known ? (
        <span className="text-zinc-900">{entry.subject}</span>
      ) : (
        // Rattachée mais inconnue de la synchronisation : le dire plutôt que
        // d'afficher une ligne vide, qui se lirait comme une perte de données.
        <span className="flex items-center gap-1 text-warn-700">
          <Icon name="alert" className="size-3.5" />
          demande inconnue de la synchronisation
        </span>
      )}

      {entry.tracker && <span className="text-xs text-zinc-500">{entry.tracker}</span>}

      {/* La provenance se lit : une note entièrement manuelle dit que la
          détection n'a rien trouvé, ce qui est un signal sur la convention de
          nommage des PR. */}
      {entry.source === 'auto' ? (
        <span className="font-mono text-xs text-zinc-400">{entry.pull_request}</span>
      ) : (
        <span className="text-xs text-zinc-400">ajoutée à la main</span>
      )}

      {canEdit && (
        <button
          type="button"
          onClick={() => remove.mutate(entry.external_id)}
          disabled={remove.isPending}
          aria-label={`Retirer la demande ${entry.external_id} de la note`}
          className="ml-auto rounded-md border border-zinc-300 bg-white px-1.5 py-0.5 text-xs font-medium text-zinc-600 transition-colors hover:bg-zinc-100 disabled:opacity-40"
        >
          Retirer
        </button>
      )}
    </li>
  )
}

/**
 * Rattachement d'une demande, choisie dans la version.
 *
 * Le geste existe parce que la détection ne peut pas tout voir : une PR dont le
 * titre ne suit pas la convention `#1234`, une demande passée en revue après la
 * création de la release. Sans lui, la seule issue serait de recréer la release.
 *
 * La liste plutôt qu'un champ de saisie : la demande cherchée appartient presque
 * toujours à la version de la release, et taper son numéro demandait de l'avoir
 * sous les yeux dans Redmine. Le champ subsiste dans la boîte, pour les demandes
 * d'une autre version — un correctif remonté d'une branche de maintenance.
 */
function AddNote({
  tag,
  available,
  error,
}: {
  tag: string
  available: NoteEntry[]
  error?: string
}) {
  const [open, setOpen] = useState(false)

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
      >
        Rattacher
      </button>

      {open && (
        <Modal title="Rattacher une demande" onClose={() => setOpen(false)} size="lg">
          <AddNoteBody tag={tag} available={available} error={error} />
        </Modal>
      )}
    </>
  )
}

function AddNoteBody({
  tag,
  available,
  error,
}: {
  tag: string
  available: NoteEntry[]
  error?: string
}) {
  const add = useAddNote(tag)
  const [needle, setNeedle] = useState('')
  const [manual, setManual] = useState('')

  const filtered = available.filter((entry) => {
    const query = needle.trim().toLowerCase()
    if (query === '') return true
    return (
      entry.external_id.toLowerCase().includes(query) ||
      (entry.subject ?? '').toLowerCase().includes(query)
    )
  })

  const submitManual = (event: React.FormEvent) => {
    event.preventDefault()
    // Le croisillon est toléré à la saisie : c'est ainsi que Redmine s'écrit
    // partout ailleurs. Il ne fait pas partie de l'identifiant.
    const reference = manual.replace(/^#/, '').trim()
    // La même forme que celle acceptée par le serveur : refuser ici évite un
    // aller-retour dont la seule issue serait une erreur.
    if (!/^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/.test(reference)) return
    add.mutate(reference, { onSuccess: () => setManual('') })
  }

  return (
    <div className="flex flex-col gap-3">
      {add.isError && (
        <p className="flex items-start gap-2 text-sm text-critical-700">
          <Icon name="alert" className="mt-0.5 size-4 shrink-0" />
          {(add.error as Error).message}
        </p>
      )}

      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-sm font-semibold text-zinc-900">
          Demandes de la version ({available.length})
        </h3>
        <input
          value={needle}
          onChange={(event) => setNeedle(event.target.value)}
          placeholder="Filtrer…"
          aria-label="Filtrer les demandes"
          className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm text-zinc-900"
        />
      </div>

      {/* Une liste vide et une lecture impossible ne se ressemblent pas : la
          première dit qu'il n'y a rien à proposer, la seconde qu'on ne sait pas.
          Les confondre annonçait que tout était déjà rattaché alors que rien
          n'avait pu être lu. */}
      {error ? (
        <p className="flex items-start gap-2 rounded-md border border-warn-500 bg-warn-50 px-3 py-2 text-sm text-warn-700">
          <Icon name="alert" className="mt-0.5 size-4 shrink-0" />
          {error} Le rattachement par numéro reste possible.
        </p>
      ) : available.length === 0 ? (
        <EmptyState>
          Aucune demande à proposer : la version n&apos;en porte pas, ou toutes sont déjà
          rattachées.
        </EmptyState>
      ) : filtered.length === 0 ? (
        <EmptyState>Aucune demande ne correspond au filtre.</EmptyState>
      ) : (
        <ul className="flex flex-col">
          {filtered.map((entry) => (
            <li
              key={entry.external_id}
              className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-zinc-200 py-1.5 text-sm first:border-t-0"
            >
              <span className="font-mono text-xs text-zinc-500">{entry.external_id}</span>
              <span className="text-zinc-900">{entry.subject}</span>
              <span className="text-xs text-zinc-500">{entry.tracker}</span>
              <span className="text-xs text-zinc-500">{entry.status}</span>
              <button
                type="button"
                onClick={() => add.mutate(entry.external_id)}
                disabled={add.isPending}
                className="ml-auto rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-40"
              >
                Rattacher
              </button>
            </li>
          ))}
        </ul>
      )}

      {/* La saisie libre reste : une demande d'une autre version — un correctif
          remonté d'une branche de maintenance — n'apparaît pas dans la liste, et
          la boîte serait alors une impasse. */}
      <form onSubmit={submitManual} className="flex items-center gap-2 border-t border-zinc-200 pt-3">
        <label htmlFor="note-manual" className="text-sm text-zinc-500">
          Demande d&apos;une autre version
        </label>
        <input
          id="note-manual"
          value={manual}
          onChange={(event) => setManual(event.target.value)}
          placeholder="1234"
          className="w-32 rounded-md border border-zinc-300 bg-white px-2 py-1 font-mono text-sm text-zinc-900"
        />
        <button
          type="submit"
          disabled={add.isPending || manual.trim() === ''}
          className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-40"
        >
          Rattacher
        </button>
      </form>
    </div>
  )
}

/**
 * Actions manuelles de la livraison, troisième volet de la note.
 *
 * Champ libre et non liste de cases à cocher : les gestes manuels d'une
 * livraison ne se répètent pas assez d'une version à l'autre pour qu'une
 * checklist les couvre, et une checklist incomplète se remplit quand même. Ce
 * texte est repris tel quel dans le bon de livraison.
 */
function ManualActions({ release, canEdit }: { release: ReleaseDetail; canEdit: boolean }) {
  const stored = release.manual_actions?.markdown ?? ''

  // La clé remonte l'éditeur quand le texte enregistré change, et seulement
  // alors. Une saisie en cours ne bouge donc pas — `stored` ne varie pas pendant
  // qu'on tape — mais une sauvegarde, ici ou ailleurs, repart du texte réel.
  // Synchroniser dans un effet ferait la même chose avec un rendu de plus et une
  // fenêtre où les deux valeurs divergent.
  return <ManualActionsEditor key={stored} release={release} stored={stored} canEdit={canEdit} />
}

function ManualActionsEditor({
  release,
  stored,
  canEdit,
}: {
  release: ReleaseDetail
  stored: string
  canEdit: boolean
}) {
  const save = useSaveManualActions(release.tag)
  const [draft, setDraft] = useState(stored)
  const dirty = draft !== stored

  // Rien à éditer et rien d'écrit : le volet disparaît. Un champ vide et
  // désactivé n'apprendrait rien à qui ne peut pas le remplir.
  if (!canEdit && stored === '') return null

  return (
    <section>
      <div className="mb-2 flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="text-sm font-semibold uppercase tracking-wide text-zinc-500">
          Actions manuelles
        </h3>
        {release.manual_actions?.updated_at && (
          <span className="text-xs text-zinc-500">
            {release.manual_actions.updated_by} —{' '}
            {formatDateTime(release.manual_actions.updated_at)}
          </span>
        )}
      </div>

      {canEdit ? (
        <>
          <label className="sr-only" htmlFor="manual-actions">
            Actions manuelles de la livraison
          </label>
          <textarea
            id="manual-actions"
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            rows={6}
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
            {save.isError && (
              <span className="text-sm text-critical-700">{(save.error as Error).message}</span>
            )}
            {dirty && !save.isPending && (
              <span className="text-sm text-zinc-500">Modifications non enregistrées.</span>
            )}
          </div>
        </>
      ) : (
        // En lecture seule, le texte tel qu'il a été écrit : c'est du Markdown
        // destiné au bon de livraison, et le rendre ici demanderait un moteur
        // pour un gain nul sur quelques lignes de consignes.
        <pre className="whitespace-pre-wrap rounded-md border border-zinc-200 bg-zinc-50 px-3 py-2 font-mono text-sm text-zinc-700">
          {stored}
        </pre>
      )}
    </section>
  )
}
