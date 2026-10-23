import type { StatusStyle } from '@arkhitekton/host'

/** L'échelle d'un avis de recette : celle d'une validation, plus l'attente. */
export type SignoffScale = 'pass' | 'warn' | 'fail' | 'na'

const SIGNOFF: Record<SignoffScale, StatusStyle> = {
  pass: {
    label: 'Validé',
    icon: 'check',
    text: 'text-healthy-700',
    bg: 'bg-healthy-50',
    border: 'border-healthy-500',
  },
  warn: {
    label: 'Réserve',
    icon: 'alert',
    text: 'text-warn-700',
    bg: 'bg-warn-50',
    border: 'border-warn-500',
  },
  fail: {
    label: 'Refusé',
    icon: 'cross',
    text: 'text-critical-700',
    bg: 'bg-critical-50',
    border: 'border-critical-500',
  },
  na: {
    label: 'En attente',
    icon: 'minus',
    text: 'text-zinc-400',
    bg: 'bg-zinc-50',
    border: 'border-zinc-200',
  },
}

/** L'apparence d'un avis de recette. */
export function signoffStyle(value: SignoffScale): StatusStyle {
  return SIGNOFF[value]
}
