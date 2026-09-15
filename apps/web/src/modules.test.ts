import { describe, expect, it } from 'vitest'

/*
 * La règle d'import, côté interface.
 *
 * Le noyau n'importe pas le dossier d'un module : il lit sa définition par
 * `src/modules.ts`. Sans cette règle, un écran du noyau pourrait appeler un hook
 * du plugin MSE, et l'éteindre laisserait des requêtes répondre 404 (doc 16 §16.6).
 */

const sources = import.meta.glob(['./**/*.ts', './**/*.tsx'], {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

const PLUGIN = './features/mse'
const ASSEMBLY = new Set(['./modules.ts', './modules.test.ts'])

function resolve(from: string, specifier: string): string {
  const parts = from.split('/').slice(0, -1)
  for (const segment of specifier.split('/')) {
    if (segment === '.') continue
    if (segment === '..') parts.pop()
    else parts.push(segment)
  }
  return parts.join('/')
}

function pluginImports(path: string, text: string): string[] {
  const found: string[] = []
  for (const [, specifier] of text.matchAll(/(?:from|import)\s*\(?\s*['"](\.{1,2}\/[^'"]+)['"]/g)) {
    if (!specifier) continue
    const target = resolve(path, specifier)
    if (target === PLUGIN || target.startsWith(`${PLUGIN}/`)) found.push(specifier)
  }
  return found
}

describe("la règle d'import des modules", () => {
  it('lit bien les sources', () => {
    expect(Object.keys(sources).length).toBeGreaterThan(50)
    // L'assemblage importe le plugin : si le contrôle ne le voyait pas, il ne
    // verrait rien d'autre non plus.
    expect(pluginImports('./modules.ts', sources['./modules.ts'] ?? '')).not.toEqual([])
  })

  it("n'autorise que l'assemblage à importer le plugin MSE", () => {
    const offenders = Object.entries(sources)
      .filter(([path]) => !path.startsWith(`${PLUGIN}/`) && !ASSEMBLY.has(path))
      .flatMap(([path, text]) => pluginImports(path, text).map((specifier) => `${path} → ${specifier}`))

    expect(offenders).toEqual([])
  })
})
