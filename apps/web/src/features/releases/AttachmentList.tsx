import { useState } from 'react'
import { formatBytes } from '../../lib/format'
import { attachmentURL, type Attachment } from './tests'

/*
 * Les fichiers qu'un résultat garde : captures, vidéos, rapports.
 *
 * Chacun s'affiche selon son type, et seulement les types qu'un navigateur
 * montre sans rien exécuter du fichier : images, vidéos, texte. Un rapport HTML
 * s'ouvre dans un cadre isolé (`sandbox` vide : ni script, ni accès à l'origine
 * du dashboard) que le serveur isole aussi de son côté. Tout le reste se
 * télécharge.
 */

type Kind = 'image' | 'video' | 'html' | 'other'

function kindOf(mediaType: string): Kind {
  const base = mediaType.split(';')[0]?.trim().toLowerCase() ?? ''
  if (base === 'image/svg+xml') return 'other'
  if (base.startsWith('image/')) return 'image'
  if (base === 'video/mp4' || base === 'video/webm') return 'video'
  if (base === 'text/html') return 'html'
  return 'other'
}

export function AttachmentList({ tag, attachments }: { tag: string; attachments: Attachment[] }) {
  if (attachments.length === 0) return null

  return (
    <ul className="flex flex-col gap-3">
      {attachments.map((attachment) => (
        <AttachmentItem key={attachment.id} tag={tag} attachment={attachment} />
      ))}
    </ul>
  )
}

function AttachmentItem({ tag, attachment }: { tag: string; attachment: Attachment }) {
  const [open, setOpen] = useState(false)
  const url = attachmentURL(tag, attachment.id)
  const kind = kindOf(attachment.media_type)

  return (
    <li className="flex flex-col gap-1.5 text-sm">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="font-mono text-zinc-900">{attachment.name}</span>
        <span className="text-zinc-500">{formatBytes(attachment.size_bytes)}</span>
        {kind === 'html' && (
          <button
            type="button"
            onClick={() => setOpen((value) => !value)}
            aria-expanded={open}
            className="rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
          >
            {open ? 'Replier' : 'Afficher'}
          </button>
        )}
        <a
          href={attachmentURL(tag, attachment.id, true)}
          className="ml-auto rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
        >
          Télécharger
        </a>
      </div>

      {kind === 'image' && (
        // Ouverte en grand dans un onglet : la vignette suffit à reconnaître
        // l'écran, l'onglet sert à le lire.
        <a href={url} target="_blank" rel="noreferrer" className="self-start">
          <img
            src={url}
            alt={attachment.name}
            loading="lazy"
            className="max-h-48 rounded-md border border-zinc-200"
          />
        </a>
      )}
      {kind === 'video' && (
        // `metadata` : la durée et la première image, pas les mégaoctets de la
        // vidéo tant que personne ne la lance.
        <video
          src={url}
          controls
          preload="metadata"
          className="max-h-80 w-full rounded-md border border-zinc-200 bg-black"
        />
      )}
      {kind === 'html' && open && (
        <iframe
          src={url}
          title={attachment.name}
          sandbox=""
          className="h-96 w-full rounded-md border border-zinc-200 bg-white"
        />
      )}
    </li>
  )
}

/** Des faits libres sur un résultat, affichés tels quels. */
export function AttributeList({ attributes }: { attributes?: Record<string, string> }) {
  const entries = Object.entries(attributes ?? {}).sort(([a], [b]) => a.localeCompare(b))
  if (entries.length === 0) return null

  return (
    <dl className="flex flex-wrap gap-x-4 gap-y-1 text-xs">
      {entries.map(([key, value]) => (
        <div key={key} className="flex gap-1">
          <dt className="text-zinc-500">{key}</dt>
          <dd className="font-mono text-zinc-700">{value}</dd>
        </div>
      ))}
    </dl>
  )
}
