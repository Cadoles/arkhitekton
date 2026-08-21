import type { ReleaseJob } from './api'

/*
 * Ce qui relie un signal du score aux pipelines qui le mesurent.
 *
 * La correspondance n'est pas de un à un, et c'est le point : `php-static-tests`
 * exécute PHPStan, PHP-CS-Fixer **et** les tests unitaires. Un run de cette
 * famille fait donc bouger deux signaux, et n'en montrer qu'un laisserait
 * l'autre au repos pendant qu'il est justement en train d'être mesuré.
 */

/** Familles de vérification relançables. Alignées sur `VerifiableKinds` côté API. */
export const VERIFIABLE_KINDS = ['static-tests', 'security-audit', 'integration-tests'] as const

export const KIND_LABEL: Record<string, string> = {
  'static-tests': 'Tests statiques',
  'security-audit': 'Audit de sécurité',
  'integration-tests': "Tests d'intégration",
  'e2e-tests': 'Tests E2E',
}

/** Les signaux qu'un pipeline mesure. */
const KIND_SIGNALS: Record<string, string[]> = {
  'static-tests': ['code', 'unit'],
  'security-audit': ['security'],
  'integration-tests': ['integration'],
  'e2e-tests': ['e2e'],
}

/** Le pipeline à relancer pour remesurer un signal. */
const SIGNAL_KIND: Record<string, string> = {
  code: 'static-tests',
  unit: 'static-tests',
  security: 'security-audit',
  integration: 'integration-tests',
}

/**
 * La famille à relancer pour remesurer ce signal, s'il en existe une.
 *
 * `e2e` et `performance` n'en ont pas : le premier vise un environnement déployé
 * et non une release, le second se mesure par une campagne de charge que le
 * dashboard ne déclenche pas. Proposer un bouton qui ne mène nulle part vaut
 * moins que ne rien proposer.
 */
export function kindForSignal(signal: string): string | undefined {
  return SIGNAL_KIND[signal]
}

/** Vrai si un pipeline mesurant ce signal est en file ou en cours. */
export function signalIsRunning(signal: string, jobs: ReleaseJob[]): boolean {
  return jobs.some(
    (job) =>
      (job.state === 'queued' || job.state === 'running') &&
      (KIND_SIGNALS[job.kind] ?? []).includes(signal),
  )
}

/**
 * Vrai si une construction est en cours.
 *
 * Distincte d'une vérification : pendant une construction, **aucun** signal n'est
 * mesurable — les tests qui suivront porteront sur une autre image. Le score
 * affiché est donc celui de l'image précédente, et le dire vaut mieux que de
 * laisser croire qu'il décrit ce qui se construit.
 */
export function buildIsRunning(jobs: ReleaseJob[]): boolean {
  return jobs.some(
    (job) => job.kind === 'build' && (job.state === 'queued' || job.state === 'running'),
  )
}
