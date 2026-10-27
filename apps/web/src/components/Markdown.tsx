import type { ReactNode } from 'react'
import { Link } from 'react-router'

/*
 * Du texte mis en forme, sans HTML : paragraphes, listes à puces, gras,
 * italique, code et liens. Le texte vient de données que d'autres écrivent ;
 * il est découpé en éléments React, jamais injecté, et un lien ne mène qu'à un
 * écran du dashboard ou à une adresse https.
 */

const INLINE = /(\*\*[^*]+\*\*|\*[^*]+\*|`[^`]+`|\[[^\]]+\]\([^)\s]+\))/g

/** Un lien sûr : un chemin du dashboard, ou une adresse https. */
export function safeHref(target: string): { internal: boolean } | undefined {
  if (target.startsWith('/') && !target.startsWith('//')) return { internal: true }
  if (target.startsWith('https://')) return { internal: false }
  return undefined
}

export function TextLink({ to, children }: { to: string; children: ReactNode }) {
  const safe = safeHref(to)
  if (!safe) return <>{children}</>
  const className = 'text-zinc-900 underline hover:text-zinc-600'
  if (safe.internal) {
    return (
      <Link to={to} className={className}>
        {children}
      </Link>
    )
  }
  return (
    <a href={to} target="_blank" rel="noreferrer noopener" className={className}>
      {children}
    </a>
  )
}

function inline(text: string): ReactNode[] {
  return text.split(INLINE).map((piece, index) => {
    if (piece.startsWith('**') && piece.endsWith('**') && piece.length > 4) {
      return <strong key={index}>{piece.slice(2, -2)}</strong>
    }
    if (piece.startsWith('`') && piece.endsWith('`') && piece.length > 2) {
      return (
        <code key={index} className="rounded bg-zinc-100 px-1 font-mono text-xs">
          {piece.slice(1, -1)}
        </code>
      )
    }
    const link = /^\[([^\]]+)\]\(([^)\s]+)\)$/.exec(piece)
    if (link) {
      return (
        <TextLink key={index} to={link[2] ?? ''}>
          {link[1]}
        </TextLink>
      )
    }
    if (piece.startsWith('*') && piece.endsWith('*') && piece.length > 2) {
      return <em key={index}>{piece.slice(1, -1)}</em>
    }
    return piece
  })
}

export function Markdown({ text }: { text: string }) {
  const blocks = text
    .split(/\n\s*\n/)
    .map((block) => block.trim())
    .filter((block) => block !== '')
  return (
    <div className="flex flex-col gap-2 text-sm text-zinc-700">
      {blocks.map((block, index) => {
        const lines = block.split('\n')
        if (lines.every((line) => /^\s*[-*] /.test(line))) {
          return (
            <ul key={index} className="list-disc pl-5">
              {lines.map((line, item) => (
                <li key={item}>{inline(line.replace(/^\s*[-*] /, ''))}</li>
              ))}
            </ul>
          )
        }
        return <p key={index}>{inline(lines.join(' '))}</p>
      })}
    </div>
  )
}
