import { useState } from 'react'
import { CollapsibleSection } from '../../components/CollapsibleSection'
import { Icon } from '../../components/Icon'
import { EmptyState, Skeleton } from '../../components/primitives'
import { hasAtLeast, useSession } from '../auth/session'
import { useAddNote, useNotes, useRemoveNote, type NoteEntry } from './api'

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
export function ReleaseNotes({ tag }: { tag: string }) {
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
      action={canEdit ? <AddNote tag={tag} /> : undefined}
    >
      {notes.length === 0 ? (
        <EmptyState>
          Aucune demande rattachée. La détection retient les demandes en revue citées par une pull
          request fusionnée&nbsp;; les autres s&apos;ajoutent par leur numéro.
        </EmptyState>
      ) : (
        <div className="flex flex-col gap-4">
          <NoteGroup title="Nouveautés" entries={features} tag={tag} canEdit={canEdit} />
          <NoteGroup title="Corrections" entries={fixes} tag={tag} canEdit={canEdit} />
        </div>
      )}
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
          <NoteRow key={entry.redmine_id} entry={entry} tag={tag} canEdit={canEdit} />
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
          #{entry.redmine_id}
        </a>
      ) : (
        <span className="font-mono text-xs text-zinc-500">#{entry.redmine_id}</span>
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
          onClick={() => remove.mutate(entry.redmine_id)}
          disabled={remove.isPending}
          aria-label={`Retirer la demande ${entry.redmine_id} de la note`}
          className="ml-auto rounded-md border border-zinc-300 bg-white px-1.5 py-0.5 text-xs font-medium text-zinc-600 transition-colors hover:bg-zinc-100 disabled:opacity-40"
        >
          Retirer
        </button>
      )}
    </li>
  )
}

/**
 * Ajout d'une demande par son numéro.
 *
 * Le geste existe parce que la détection ne peut pas tout voir : une PR dont le
 * titre ne suit pas la convention `#1234`, une demande passée en revue après la
 * création de la release. Sans lui, la seule issue serait de recréer la release.
 */
function AddNote({ tag }: { tag: string }) {
  const add = useAddNote(tag)
  const [value, setValue] = useState('')

  const submit = (event: React.FormEvent) => {
    event.preventDefault()
    const number = Number(value.replace(/^#/, '').trim())
    if (!Number.isInteger(number) || number <= 0) return
    add.mutate(number, { onSuccess: () => setValue('') })
  }

  return (
    <form onSubmit={submit} className="flex items-center gap-1">
      <input
        value={value}
        onChange={(event) => setValue(event.target.value)}
        placeholder="#1234"
        aria-label="Numéro de demande Redmine à rattacher"
        className="w-24 rounded-md border border-zinc-300 bg-white px-2 py-1 font-mono text-sm text-zinc-900"
      />
      <button
        type="submit"
        disabled={add.isPending || value.trim() === ''}
        className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-40"
      >
        Rattacher
      </button>
      {add.isError && (
        <span className="flex items-center gap-1 text-xs text-critical-700">
          <Icon name="alert" className="size-3.5" />
          {(add.error as Error).message}
        </span>
      )}
    </form>
  )
}
