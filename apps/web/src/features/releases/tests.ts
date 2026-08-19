import { useQuery } from '@tanstack/react-query'
import { request } from '../../lib/api'
import type { Health, PipelineStatus } from '../../lib/status'

export type TestResult = {
  id: number
  family: string
  tool: string
  status: PipelineStatus
  total: number
  passed: number
  failed: number
  skipped: number
  flaky: number
  duration_ms: number
  summary?: string
  measured_at: string
}

export type SignalScore = {
  signal: string
  value: number
  /** Poids **effectif**, après redistribution des signaux non mesurés. C'est lui
   *  qui explique la contribution réelle au score. */
  weight: number
  measured: boolean
  detail?: string
  /** Verdict du signal, rendu par le serveur. L'interface ne dérive aucun
   *  seuil : le faire produirait un second avis, divergent au premier
   *  changement côté serveur (B11). */
  health: Health | 'na'
}

export type ScoreBreakdown = {
  value: number
  measured: boolean
  health: Health | 'na'
  signals: SignalScore[]
  /** Signaux sortis du calcul. Affichés tels quels : le lecteur doit savoir sur
   *  quoi porte le score qu'il lit — un 100 sur trois signaux n'est pas un 100
   *  sur six (B11). */
  redistributed: string[]
}

export function useTests(tag: string | undefined) {
  return useQuery({
    queryKey: ['releases', tag, 'tests'],
    queryFn: () =>
      request<{ results: TestResult[]; score: ScoreBreakdown }>(
        `/releases/${encodeURIComponent(tag as string)}/tests`,
      ),
    enabled: Boolean(tag),
  })
}

export type CaseStatus = 'passed' | 'failed' | 'skipped' | 'flaky'
export type Change = 'new-failure' | 'fixed' | 'added' | 'removed'

export type TestCase = {
  suite: string
  name: string
  status: CaseStatus
  duration_ms: number
  message?: string
  trace?: string
}

export type CaseDiff = {
  suite: string
  name: string
  status?: CaseStatus
  was?: CaseStatus
  change: Change
}

export type FamilyPage = {
  family: string
  cases: TestCase[]
  diff?: CaseDiff[]
  /** Nombre de cas correspondant au filtre, toutes pages confondues. */
  total: number
  limit: number
  offset: number
}

/**
 * Une page de cas d'une famille.
 *
 * `offset` et `query` entrent dans la clé de cache : deux pages sont deux
 * ressources distinctes, et les confondre ferait afficher la première pour la
 * seconde le temps d'un aller-retour.
 *
 * `placeholderData` garde la page précédente affichée pendant le chargement de la
 * suivante. Sans elle, chaque changement de page vide la table puis la remplit,
 * ce qui fait sauter la mise en page à chaque clic.
 */
export function useFamily(
  tag: string | undefined,
  family: string | undefined,
  offset = 0,
  query = '',
) {
  return useQuery({
    queryKey: ['releases', tag, 'tests', family, { offset, query }],
    queryFn: () => {
      const params = new URLSearchParams()
      if (offset > 0) params.set('offset', String(offset))
      if (query) params.set('q', query)
      const suffix = params.size > 0 ? `?${params}` : ''
      return request<FamilyPage>(
        `/releases/${encodeURIComponent(tag as string)}/tests/${encodeURIComponent(family as string)}${suffix}`,
      )
    },
    enabled: Boolean(tag && family),
    placeholderData: (previous) => previous,
  })
}

export const SIGNAL_LABEL: Record<string, string> = {
  unit: 'Tests unitaires',
  integration: "Tests d'intégration",
  e2e: 'Tests E2E',
  code: 'Qualité du code',
  security: 'Sécurité',
  performance: 'Performance',
}

export const FAMILY_LABEL: Record<string, string> = {
  unit: 'Tests unitaires',
  integration: "Tests d'intégration",
  e2e: 'Tests E2E',
  static: 'Analyse statique',
  security: 'Sécurité',
}

export const CHANGE_LABEL: Record<Change, string> = {
  'new-failure': 'Nouvel échec',
  fixed: 'Échec résolu',
  added: 'Test ajouté',
  removed: 'Test supprimé',
}

export type Severity = 'CRITICAL' | 'HIGH' | 'MEDIUM' | 'LOW' | 'INFO'

export type Finding = {
  fingerprint: string
  severity: Severity
  title: string
  tool: string
  category: string
  rule_id: string
  cvss?: number
  target?: string
  location?: string
  package?: string
  installed?: string
  fixed?: string
  fixable: boolean
  /** `new`, `existing` ou `resolved`, établi par le manager en comparant au run
   *  précédent. C'est ce qui distingue une régression d'une dette connue. */
  status: string
  description?: string
  suppressed: boolean
  suppression_reason?: string
  suppression_expires?: string
}

export type SecurityReport = {
  audited: boolean
  run_id?: string
  /** Faux quand un scanner n'a pas conclu. Les compteurs restent affichés mais
   *  ne notent rien : un audit partiel qui afficherait « conforme » est le pire
   *  résultat possible. */
  measured?: boolean
  failed_scanners?: string[]
  scanner_status?: Record<string, string>
  counts?: { total: number; critical: number; high: number; medium: number; low: number; suppressed: number }
  delta?: { new: number; existing: number; resolved: number }
  previous_run_id?: string
  policy?: Record<string, string>
  generated_at?: string
  findings?: Finding[]
}

export function useSecurity(tag: string | undefined) {
  return useQuery({
    queryKey: ['releases', tag, 'security'],
    queryFn: () => request<SecurityReport>(`/releases/${encodeURIComponent(tag as string)}/security`),
    enabled: Boolean(tag),
  })
}

export const SEVERITY_LABEL: Record<Severity, string> = {
  CRITICAL: 'Critique',
  HIGH: 'Haute',
  MEDIUM: 'Moyenne',
  LOW: 'Faible',
  INFO: 'Information',
}
