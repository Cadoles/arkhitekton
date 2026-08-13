/*
 * Vocabulaire et verdicts — repris de la maquette sans modification.
 *
 * Ce module ne contient aucune logique de framework : ce sont des seuils et des
 * tables de libellés, c'est-à-dire des décisions métier. Les recopier tels quels
 * est ce qui garantit que l'interface portée dit exactement la même chose que la
 * maquette validée (B9).
 *
 * Chaque entrée porte une icône en plus des couleurs : un statut ne doit jamais
 * être signalé par la seule couleur (doc 08 §8.5).
 */

import type { IconName } from '../components/Icon'

export type Health = 'healthy' | 'warn' | 'critical'
export type PipelineStatus = 'pass' | 'warn' | 'fail' | 'skip' | 'na'
export type BenchStatus = 'pass' | 'warn' | 'fail' | 'na'
export type SignoffStatus = 'pass' | 'warn' | 'fail' | 'na'
export type SecuritySeverity = 'high' | 'moderate' | 'medium' | 'low'
export type IssueSeverity = 'high' | 'medium' | 'low'

/** Apparence d'un statut : libellé français, icône, et classes Tailwind. */
export type StatusStyle = {
  label: string
  icon: IconName
  text: string
  bg: string
  border: string
}

const HEALTH_THRESHOLDS = { healthy: 85, warn: 70 } as const

/** Verdict de santé à partir du score agrégé d'une release. */
export function healthFromScore(score: number): Health {
  if (score >= HEALTH_THRESHOLDS.healthy) return 'healthy'
  if (score >= HEALTH_THRESHOLDS.warn) return 'warn'
  return 'critical'
}

const RANK = { na: 0, pass: 1, warn: 2, fail: 3 } as const

function compare(value: number, baseline: number, lowerBetter: boolean): BenchStatus {
  const ratio = lowerBetter ? baseline / value : value / baseline
  if (ratio >= 1) return 'pass'
  if (ratio >= 0.9) return 'warn'
  return 'fail'
}

export type BenchMeasure = {
  value: number | null
  baseline: number
  lowerBetter: boolean
  total?: number | null
  totalBaseline?: number | null
}

/**
 * Verdict d'un bench.
 *
 * Le volume total peut **dégrader** le verdict de débit, jamais l'améliorer : un
 * débit instantané conforme obtenu en traitant moins de requêtes n'est pas un
 * succès. D'où la comparaison de rangs plutôt qu'un simple `&&`.
 */
export function benchVerdict(measure: BenchMeasure): BenchStatus {
  if (measure.value === null) return 'na'

  let verdict = compare(measure.value, measure.baseline, measure.lowerBetter)

  if (measure.total != null && measure.totalBaseline != null) {
    const onVolume = compare(measure.total, measure.totalBaseline, false)
    if (RANK[onVolume] > RANK[verdict]) verdict = onVolume
  }
  return verdict
}

const HEALTH: Record<Health, StatusStyle> = {
  healthy: { label: 'Sain', icon: 'check', text: 'text-healthy-700', bg: 'bg-healthy-50', border: 'border-healthy-500' },
  warn: { label: 'À surveiller', icon: 'alert', text: 'text-warn-700', bg: 'bg-warn-50', border: 'border-warn-500' },
  critical: { label: 'Critique', icon: 'cross', text: 'text-critical-700', bg: 'bg-critical-50', border: 'border-critical-500' },
}

const PIPELINE: Record<PipelineStatus, StatusStyle> = {
  pass: { label: 'Réussi', icon: 'check', text: 'text-healthy-700', bg: 'bg-healthy-50', border: 'border-healthy-500' },
  warn: { label: 'Alerte', icon: 'alert', text: 'text-warn-700', bg: 'bg-warn-50', border: 'border-warn-500' },
  fail: { label: 'Échec', icon: 'cross', text: 'text-critical-700', bg: 'bg-critical-50', border: 'border-critical-500' },
  skip: { label: 'Ignoré', icon: 'skip', text: 'text-zinc-500', bg: 'bg-zinc-100', border: 'border-zinc-300' },
  na: { label: 'N/A', icon: 'minus', text: 'text-zinc-400', bg: 'bg-zinc-50', border: 'border-zinc-200' },
}

const BENCH: Record<BenchStatus, StatusStyle> = {
  pass: { label: 'Conforme', icon: 'check', text: 'text-healthy-700', bg: 'bg-healthy-50', border: 'border-healthy-500' },
  warn: { label: 'Limite', icon: 'alert', text: 'text-warn-700', bg: 'bg-warn-50', border: 'border-warn-500' },
  fail: { label: 'Hors seuil', icon: 'cross', text: 'text-critical-700', bg: 'bg-critical-50', border: 'border-critical-500' },
  na: { label: 'Non mesuré', icon: 'minus', text: 'text-zinc-400', bg: 'bg-zinc-50', border: 'border-zinc-200' },
}

const SIGNOFF: Record<SignoffStatus, StatusStyle> = {
  pass: { label: 'Validé', icon: 'check', text: 'text-healthy-700', bg: 'bg-healthy-50', border: 'border-healthy-500' },
  warn: { label: 'Réserve', icon: 'alert', text: 'text-warn-700', bg: 'bg-warn-50', border: 'border-warn-500' },
  fail: { label: 'Refusé', icon: 'cross', text: 'text-critical-700', bg: 'bg-critical-50', border: 'border-critical-500' },
  na: { label: 'En attente', icon: 'minus', text: 'text-zinc-400', bg: 'bg-zinc-50', border: 'border-zinc-200' },
}

const SECURITY: Record<SecuritySeverity, StatusStyle> = {
  high: { label: 'Haute', icon: 'cross', text: 'text-critical-700', bg: 'bg-critical-50', border: 'border-critical-500' },
  moderate: { label: 'Modérée', icon: 'alert', text: 'text-warn-700', bg: 'bg-warn-50', border: 'border-warn-500' },
  medium: { label: 'Moyenne', icon: 'alert', text: 'text-warn-700', bg: 'bg-warn-50', border: 'border-warn-500' },
  low: { label: 'Faible', icon: 'minus', text: 'text-zinc-600', bg: 'bg-zinc-100', border: 'border-zinc-300' },
}

const ISSUE: Record<IssueSeverity, StatusStyle> = {
  high: { label: 'Haute', icon: 'cross', text: 'text-critical-700', bg: 'bg-critical-50', border: 'border-critical-500' },
  medium: { label: 'Moyenne', icon: 'alert', text: 'text-warn-700', bg: 'bg-warn-50', border: 'border-warn-500' },
  low: { label: 'Faible', icon: 'minus', text: 'text-zinc-600', bg: 'bg-zinc-100', border: 'border-zinc-300' },
}

export const status = {
  health: (value: Health) => HEALTH[value],
  pipeline: (value: PipelineStatus) => PIPELINE[value],
  bench: (value: BenchStatus) => BENCH[value],
  signoff: (value: SignoffStatus) => SIGNOFF[value],
  security: (value: SecuritySeverity) => SECURITY[value],
  issue: (value: IssueSeverity) => ISSUE[value],
}
