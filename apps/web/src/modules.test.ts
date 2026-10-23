import { describe, expect, it } from 'vitest'

/*
 * Les règles d'import des modules, côté interface.
 *
 * Le noyau n'importe aucun module : il les découvre par `src/modules.ts`. Sans
 * cette règle, un écran du noyau pourrait appeler un hook d'un plugin, et
 * l'éteindre laisserait des requêtes répondre 404.
 *
 * Un module n'importe que l'API hôte (`@arkhitekton/host`), React, le routeur,
 * react-query et ses propres fichiers : c'est ce qui lui permet de vivre dans un
 * autre dépôt et d'être déposé dans celui-ci au moment du build.
 */

const sources = import.meta.glob(['./**/*.ts', './**/*.tsx'], {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

const PLUGINS = './plugins/'
const ALLOWED = new Set(['@arkhitekton/host', 'react', 'react-router', '@tanstack/react-query'])

function resolve(from: string, specifier: string): string {
  const parts = from.split('/').slice(0, -1)
  for (const segment of specifier.split('/')) {
    if (segment === '.') continue
    if (segment === '..') parts.pop()
    else parts.push(segment)
  }
  return parts.join('/')
}

function specifiers(text: string): string[] {
  return [...text.matchAll(/(?:from|import)\s*\(?\s*['"]([^'"]+)['"]/g)].flatMap(([, specifier]) =>
    specifier ? [specifier] : [],
  )
}

describe("les règles d'import des modules", () => {
  it('lit bien les sources, et au moins un module', () => {
    expect(Object.keys(sources).length).toBeGreaterThan(50)
    expect(Object.keys(sources).some((path) => path.startsWith(PLUGINS))).toBe(true)
  })

  it("n'autorise aucun fichier du noyau à importer un module", () => {
    const offenders = Object.entries(sources)
      .filter(([path]) => !path.startsWith(PLUGINS))
      .flatMap(([path, text]) =>
        specifiers(text)
          .filter(
            (specifier) =>
              specifier.startsWith('.') && resolve(path, specifier).startsWith(PLUGINS),
          )
          .map((specifier) => `${path} → ${specifier}`),
      )

    expect(offenders).toEqual([])
  })

  it("ne laisse un module importer que l'API hôte, ses bibliothèques et ses propres fichiers", () => {
    const offenders = Object.entries(sources)
      .filter(([path]) => path.startsWith(PLUGINS))
      .flatMap(([path, text]) => {
        const own = path.split('/').slice(0, 3).join('/')
        return specifiers(text)
          .filter((specifier) =>
            specifier.startsWith('.')
              ? !resolve(path, specifier).startsWith(`${own}/`)
              : !ALLOWED.has(specifier),
          )
          .map((specifier) => `${path} → ${specifier}`)
      })

    expect(offenders).toEqual([])
  })
})
