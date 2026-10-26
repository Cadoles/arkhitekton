/*
 * Ce qu'un module de l'interface emprunte au noyau.
 *
 * Un module (`src/plugins/<id>/`) n'importe que ce fichier, sous l'alias
 * `@arkhitekton/host`, plus `react`, `react-router` et `@tanstack/react-query` :
 * c'est ce qui lui permet de vivre dans un autre dépôt et d'être déposé dans
 * celui-ci au moment de construire une distribution. Un test le vérifie
 * (`modules.test.ts`).
 *
 * Contrat encore jeune : jusqu'à arkhitekton 1.0, une version mineure peut le
 * changer, et le dit dans ses notes.
 */

export type { AppModule, ModuleNavEntry } from '../lib/modules'
export { registerModule } from '../lib/registry'
export { request, ApiError } from '../lib/api'
export { projectPath } from '../lib/project'
export { formatBytes, formatDate, formatDateTime } from '../lib/format'
export type { StatusStyle } from '../lib/status'
export { Icon } from '../components/Icon'
export type { IconName } from '../components/Icon'
export { Card, EmptyState, ErrorState, Skeleton, StatusBadge } from '../components/primitives'
export { CollapsibleSection } from '../components/CollapsibleSection'
export { ConfirmDialog } from '../components/ConfirmDialog'
export type { Promotion, Release, ReleaseDetail } from '../features/releases/api'
export { useReleases } from '../features/releases/api'
export { TestsPanel } from '../features/releases/TestsPanel'
export { SIGNAL_LABEL, useTests } from '../features/releases/tests'
export type { Role } from '../features/auth/session'
export { hasAtLeast, useSession } from '../features/auth/session'
