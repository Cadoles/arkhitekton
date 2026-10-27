import { formatAge, formatBytes, formatDate, formatDateTime } from './format'

/*
 * Les gabarits d'affichage des écrans décrits en données : `{chemin | filtre}`.
 *
 * Même grammaire que `sdk/declare` côté serveur, qui les valide à
 * l'installation ; ici, on les rend avec les données chargées par le
 * navigateur. Les deux lisent les mêmes cas (`sdk/declare/testdata/display.json`),
 * et c'est ce qui les garde d'accord.
 *
 * Fermée exprès : un chemin, des filtres, pas de boucle ni de condition. Ce qui
 * n'y tient pas est un composant, pas un mot de plus ici.
 */

/** Les données qu'un gabarit lit, de la plus proche à la plus lointaine : la
 *  ligne d'un tableau d'abord, puis les données du bloc, puis la route. */
export type Scope = Record<string, unknown>[]

type Argument = { value: string; literal: boolean }
type Filter = { name: string; args: Argument[] }
type Part = { literal: string } | { path: string; filters: Filter[] }

const PATH = /^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z0-9_]+)*$/

/** Le nombre d'arguments de chaque filtre, au plus. */
const FILTERS: Record<string, number> = {
  firstline: 0,
  trim: 0,
  lower: 0,
  upper: 0,
  or: 1,
  date: 0,
  datetime: 0,
  age: 0,
  bytes: 0,
  count: 0,
  join: 1,
}

export class TemplateError extends Error {}

const parsed = new Map<string, Part[]>()

/** Lit un gabarit, ou lève en nommant ce qui ne va pas. */
export function parseTemplate(source: string): Part[] {
  const known = parsed.get(source)
  if (known) return known
  const parts: Part[] = []
  let rest = source
  while (rest !== '') {
    const open = rest.indexOf('{')
    const close = rest.indexOf('}')
    if (close >= 0 && (open < 0 || close < open)) {
      throw new TemplateError(`gabarit « ${source} » : une « } » ne ferme rien`)
    }
    if (open < 0) {
      parts.push({ literal: rest })
      break
    }
    if (open > 0) parts.push({ literal: rest.slice(0, open) })
    const end = rest.indexOf('}', open)
    if (end < 0) throw new TemplateError(`gabarit « ${source} » : une « { » n'est pas fermée`)
    parts.push(parseExpression(rest.slice(open + 1, end), source))
    rest = rest.slice(end + 1)
  }
  parsed.set(source, parts)
  return parts
}

function parseExpression(expression: string, source: string): Part {
  const [head = '', ...segments] = splitOutsideQuotes(expression, '|')
  const path = head.trim()
  if (!PATH.test(path)) throw new TemplateError(`gabarit « ${source} » : « ${path} » n'est pas un chemin`)
  const filters = segments.map((segment) => {
    const trimmed = segment.trim()
    const colon = trimmed.indexOf(':')
    const name = (colon < 0 ? trimmed : trimmed.slice(0, colon)).trim()
    const args = colon < 0 ? [] : parseArguments(trimmed.slice(colon + 1), source)
    const arity = FILTERS[name]
    if (arity === undefined) throw new TemplateError(`gabarit « ${source} » : filtre inconnu « ${name} »`)
    if (name === 'or') {
      const [arg] = args
      if (args.length !== 1 || !arg || (!arg.literal && !PATH.test(arg.value))) {
        throw new TemplateError(`gabarit « ${source} » : or prend un chemin ou un 'texte'`)
      }
    } else if (args.length > arity || args.some((arg) => !arg.literal)) {
      throw new TemplateError(`gabarit « ${source} » : ${name} prend au plus ${arity} 'texte'`)
    }
    return { name, args }
  })
  return { path, filters }
}

function parseArguments(source: string, template: string): Argument[] {
  const args: Argument[] = []
  let rest = source.trim()
  while (rest !== '') {
    if (rest.startsWith("'")) {
      const end = rest.indexOf("'", 1)
      if (end < 0) throw new TemplateError(`gabarit « ${template} » : une apostrophe n'est pas fermée`)
      args.push({ value: rest.slice(1, end), literal: true })
      rest = rest.slice(end + 1).trim()
      continue
    }
    const space = rest.indexOf(' ')
    args.push({ value: space < 0 ? rest : rest.slice(0, space), literal: false })
    rest = space < 0 ? '' : rest.slice(space + 1).trim()
  }
  return args
}

function splitOutsideQuotes(source: string, separator: string): string[] {
  const segments: string[] = []
  let quoted = false
  let start = 0
  for (let i = 0; i < source.length; i++) {
    if (source[i] === "'") quoted = !quoted
    else if (source[i] === separator && !quoted) {
      segments.push(source.slice(start, i))
      start = i + 1
    }
  }
  segments.push(source.slice(start))
  return segments
}

/** Les chemins qu'un gabarit lit. */
export function templateFields(source: string): string[] {
  return parseTemplate(source).flatMap((part) =>
    'path' in part
      ? [
          part.path,
          ...part.filters.flatMap((filter) =>
            filter.name === 'or' && filter.args[0] && !filter.args[0].literal ? [filter.args[0].value] : [],
          ),
        ]
      : [],
  )
}

/** La valeur d'un chemin : le premier niveau de la portée qui porte sa tête. */
export function lookup(scope: Scope, path: string): unknown {
  const [head = '', ...rest] = path.split('.')
  const holder = scope.find((level) => level !== null && typeof level === 'object' && head in level)
  let value: unknown = holder?.[head]
  for (const key of rest) {
    if (value === null || typeof value !== 'object') return undefined
    value = (value as Record<string, unknown>)[key]
  }
  return value
}

/** Une valeur telle qu'elle s'écrit : un objet ne s'écrit pas. */
export function formatValue(value: unknown): string {
  if (value === null || value === undefined) return ''
  if (typeof value === 'string') return value
  if (typeof value === 'number') return String(value)
  if (typeof value === 'boolean') return value ? 'oui' : 'non'
  if (Array.isArray(value)) return value.map(formatValue).join(', ')
  return ''
}

function apply(filter: Filter, value: unknown, scope: Scope): unknown {
  switch (filter.name) {
    case 'firstline':
      return (
        formatValue(value)
          .split('\n')
          .map((line) => line.trim())
          .find((line) => line !== '') ?? ''
      )
    case 'trim':
      return formatValue(value).trim()
    case 'lower':
      return formatValue(value).toLowerCase()
    case 'upper':
      return formatValue(value).toUpperCase()
    case 'or': {
      if (formatValue(value) !== '') return value
      const [arg] = filter.args
      if (!arg) return value
      return arg.literal ? arg.value : lookup(scope, arg.value)
    }
    case 'date':
      return formatValue(value) === '' ? '' : formatDate(formatValue(value))
    case 'datetime':
      return formatValue(value) === '' ? '' : formatDateTime(formatValue(value))
    case 'age':
      return formatValue(value) === '' ? '' : formatAge(formatValue(value))
    case 'bytes':
      return typeof value === 'number' ? formatBytes(value) : value
    case 'count':
      return Array.isArray(value) ? value.length : ''
    case 'join':
      return Array.isArray(value) ? value.map(formatValue).join(filter.args[0]?.value ?? ', ') : value
  }
  return value
}

/** Rend un gabarit. Un gabarit mal formé ne devrait pas arriver jusqu'ici, le
 *  serveur l'ayant refusé : il se rend alors tel quel plutôt que de faire
 *  tomber l'écran. */
export function renderTemplate(source: string | undefined, scope: Scope): string {
  if (!source) return ''
  let parts: Part[]
  try {
    parts = parseTemplate(source)
  } catch {
    return source
  }
  return parts
    .map((part) => {
      if ('literal' in part) return part.literal
      let value = lookup(scope, part.path)
      for (const filter of part.filters) value = apply(filter, value, scope)
      return formatValue(value)
    })
    .join('')
}
