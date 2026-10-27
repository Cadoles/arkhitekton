import { describe, expect, it } from 'vitest'
import display from '../../../../sdk/declare/testdata/display.json?raw'
import { parseTemplate, renderTemplate, templateFields } from './template'

/*
 * Les cas que le serveur lit aussi (`sdk/declare`) : une grammaire, deux
 * implémentations, qui doivent dire la même chose.
 */
type Vectors = {
  parse: { template: string; fields?: string[]; error?: boolean }[]
  render: { template: string; data: Record<string, unknown>; expected: string }[]
}
const vectors = JSON.parse(display) as Vectors

describe('gabarits d’affichage', () => {
  it.each(vectors.parse)('lit « $template » comme le serveur', ({ template, fields, error }) => {
    if (error) {
      expect(() => parseTemplate(template)).toThrow()
      return
    }
    expect(templateFields(template)).toEqual(fields ?? [])
  })

  it.each(vectors.render)('rend « $template »', ({ template, data, expected }) => {
    expect(renderTemplate(template, [data])).toBe(expected)
  })

  it('lit la ligne avant les données du bloc, et la route en dernier', () => {
    const scope = [{ label: 'Prod' }, { label: 'masqué', release: { tag: '3.4.0' } }, { route: { tag: 'x' } }]
    expect(renderTemplate('{label} {release.tag} {route.tag}', scope)).toBe('Prod 3.4.0 x')
  })

  it('rend une date et un âge', () => {
    const recent = new Date(Date.now() - 5 * 60_000).toISOString()
    expect(renderTemplate('{since | age}', [{ since: recent }])).toBe('il y a 5 min')
    expect(renderTemplate('{when | date}', [{ when: '2026-03-04T10:00:00Z' }])).toMatch(/2026/)
    expect(renderTemplate('{when | date}', [{}])).toBe('')
  })

  it('rend tel quel un gabarit mal formé plutôt que de lever', () => {
    expect(renderTemplate('{broken', [{}])).toBe('{broken')
  })
})
