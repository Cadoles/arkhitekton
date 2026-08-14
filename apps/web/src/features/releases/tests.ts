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

export function useFamily(tag: string | undefined, family: string | undefined) {
  return useQuery({
    queryKey: ['releases', tag, 'tests', family],
    queryFn: () =>
      request<{ family: string; cases: TestCase[]; diff?: CaseDiff[] }>(
        `/releases/${encodeURIComponent(tag as string)}/tests/${encodeURIComponent(family as string)}`,
      ),
    enabled: Boolean(tag && family),
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
