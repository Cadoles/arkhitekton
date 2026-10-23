/*
 * Vocabulaire de l'interface — libellés français et apparence des statuts.
 *
 * Repris de la maquette (B9), mais réduit à ce qu'il porte réellement : des
 * libellés et des classes. Les seuils, eux, ont quitté ce module.
 *
 * La maquette calculait ses verdicts dans le navigateur, faute de serveur. Ici
 * ils viennent de l'API — `healthFromScore` en Go dans `internal/results/score.go`
 * avec les mêmes 85/70, le verdict de bench dans `internal/bench/compare.go` avec
 * la même dégradation par le volume total. Garder une seconde implémentation ici
 * n'aurait pas servi l'interface : elle était morte, et la réveiller aurait
 * produit un second avis, divergent au premier changement de seuil. Le calcul
 * appartient au serveur, qui est aussi le seul à connaître les conditions de
 * mesure (B16) et les signaux non mesurés (B11).
 *
 * La grammaire des sévérités a divergé pour une raison de fond : la maquette
 * parlait `high|moderate|low`, celle de `npm audit`, alors que les scanners
 * réellement branchés — Trivy, govulncheck — parlent
 * `CRITICAL|HIGH|MEDIUM|LOW|INFO`. La table de la maquette ne savait pas dire
 * `CRITICAL`, la plus grave. Le vocabulaire de sécurité vit donc dans
 * `features/releases/tests.ts`, au contact du format qu'il décrit.
 *
 * Chaque entrée porte une icône en plus des couleurs : un statut ne doit jamais
 * être signalé par la seule couleur (doc 08 §8.5).
 */

import type { IconName } from '../components/Icon'

export type Health = 'healthy' | 'warn' | 'critical'
export type PipelineStatus = 'pass' | 'warn' | 'fail' | 'skip' | 'na'
export type BenchStatus = 'pass' | 'warn' | 'fail' | 'na'
export type ReleaseState =
  | 'draft'
  | 'building'
  | 'verifying'
  | 'ready'
  | 'released'
  | 'failed'
  | 'cancelled'

/** Apparence d'un statut : libellé français, icône, et classes Tailwind. */
export type StatusStyle = {
  label: string
  icon: IconName
  text: string
  bg: string
  border: string
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

/*
 * États d'une release.
 *
 * Les libellés disent l'attente, pas seulement l'état : « décision en attente »
 * se lit mieux que « prête » pour un état qui n'autorise justement pas encore la
 * promotion.
 *
 * Trois états en cours — construction, tests, création — restent neutres : ils ne
 * portent aucun verdict, et les teinter d'alerte ferait lire comme un problème ce
 * qui n'est qu'un travail en cours.
 */
const RELEASE: Record<ReleaseState, StatusStyle> = {
  draft: { label: 'Créée', icon: 'doc', text: 'text-zinc-600', bg: 'bg-zinc-50', border: 'border-zinc-300' },
  building: { label: 'Construction en cours', icon: 'cog', text: 'text-zinc-600', bg: 'bg-zinc-50', border: 'border-zinc-300' },
  verifying: { label: 'Tests en cours', icon: 'beaker', text: 'text-zinc-600', bg: 'bg-zinc-50', border: 'border-zinc-300' },
  ready: { label: 'Décision en attente', icon: 'alert', text: 'text-warn-700', bg: 'bg-warn-50', border: 'border-warn-500' },
  // « Validée » et non « Livrée » : cet état dit qu'une décision humaine a été
  // prise et que l'image est promouvable, pas qu'elle tourne quelque part. Le
  // déploiement est un autre axe — il se lit aux pastilles d'environnement, qui
  // viennent de ce que chaque environnement rapporte. Une release validée que
  // personne n'a promue n'est déployée nulle part, et « Livrée » le laissait
  // croire.
  released: { label: 'Validée', icon: 'check', text: 'text-healthy-700', bg: 'bg-healthy-50', border: 'border-healthy-500' },
  failed: { label: 'Échec', icon: 'cross', text: 'text-critical-700', bg: 'bg-critical-50', border: 'border-critical-500' },
  cancelled: { label: 'Abandonnée', icon: 'skip', text: 'text-zinc-500', bg: 'bg-zinc-100', border: 'border-zinc-300' },
}

export const status = {
  release: (value: ReleaseState) => RELEASE[value],
  health: (value: Health) => HEALTH[value],
  pipeline: (value: PipelineStatus) => PIPELINE[value],
  bench: (value: BenchStatus) => BENCH[value],
}
