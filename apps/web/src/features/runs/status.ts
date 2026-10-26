import type { StatusStyle } from '../../lib/status'
import type { RunStatus } from './api'

/*
 * Apparence des statuts d'exécution.
 *
 * Les classes sont celles des tables de la maquette : un run réussi a la même
 * couleur et la même icône qu'un pipeline réussi, parce que c'est la même
 * information. Ce qui change est le vocabulaire — on parle d'exécution, pas de
 * résultat de test.
 */

const RUN_STATUS: Record<RunStatus, StatusStyle> = {
  pending: { label: 'En attente', icon: 'clock', text: 'text-zinc-500', bg: 'bg-zinc-100', border: 'border-zinc-300' },
  running: { label: 'En cours', icon: 'clock', text: 'text-zinc-700', bg: 'bg-zinc-100', border: 'border-zinc-400' },
  succeeded: { label: 'Réussie', icon: 'check', text: 'text-healthy-700', bg: 'bg-healthy-50', border: 'border-healthy-500' },
  failed: { label: 'Échouée', icon: 'cross', text: 'text-critical-700', bg: 'bg-critical-50', border: 'border-critical-500' },
  // Annulée n'est pas un échec : c'est une décision. La confondre avec un échec
  // enverrait chercher une cause technique qui n'existe pas.
  cancelled: { label: 'Annulée', icon: 'skip', text: 'text-zinc-500', bg: 'bg-zinc-100', border: 'border-zinc-300' },
  timedout: { label: 'Expirée', icon: 'alert', text: 'text-warn-700', bg: 'bg-warn-50', border: 'border-warn-500' },
}

export function runStatus(status: RunStatus): StatusStyle {
  return RUN_STATUS[status] ?? RUN_STATUS.pending
}
