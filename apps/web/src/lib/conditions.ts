import type { Condition } from './interface'
import { lookup, type Scope } from './template'

/*
 * Les conditions d'un bloc ou d'un widget : toutes vraies, ou rien n'est rendu.
 * Elles portent sur les données ; la permission et l'activation du plugin sont
 * déjà passées par le serveur.
 */

function same(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b)
}

function present(value: unknown): boolean {
  if (value === null || value === undefined || value === '') return false
  if (Array.isArray(value)) return value.length > 0
  return true
}

export function holds(conditions: Condition[] | undefined, scope: Scope): boolean {
  return (conditions ?? []).every((condition) => {
    const value = lookup(scope, condition.field)
    if (condition.present !== undefined) return present(value) === condition.present
    if (condition.in !== undefined) return condition.in.some((candidate) => same(value, candidate))
    return same(value, condition.equals)
  })
}
