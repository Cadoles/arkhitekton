import { describe, expect, it } from 'vitest'
import { layerPluginStyles } from './pluginLayers'

describe("la feuille d'un plugin", () => {
  it('range ses utilitaires de base sous ceux du noyau, et garde ses variantes au-dessus', () => {
    const css =
      '@layer theme;@layer utilities{.flex{display:flex}.hidden{display:none}' +
      '.hover\\:bg-white:hover{background:#fff}' +
      '@media (min-width:64rem){.lg\\:flex{display:flex}}' +
      '.\\[scrollbar-gutter\\:stable\\]{scrollbar-gutter:stable}}'

    const out = layerPluginStyles(css)

    expect(out).toContain('@layer components{.flex{display:flex}.hidden{display:none}}')
    expect(out).toMatch(/@layer utilities\{\.hover\\:bg-white:hover\{background:#fff\}@media/)
    expect(out).toContain('.lg\\:flex{display:flex}')
    expect(out.indexOf('@layer components')).toBeLessThan(out.indexOf('@layer utilities'))
  })

  it('ne touche à rien hors de la couche des utilitaires', () => {
    const css = '@layer properties{*{--tw-x:0}}@layer theme;'
    expect(layerPluginStyles(css)).toBe(css)
  })
})
