import { lazy } from 'react'
import { AppShell } from '../components/AppShell'
import { HomeRedirect } from '../components/ModuleRoute'
import { NotFound } from '../components/NotFound'
import { Placeholder } from '../components/Placeholder'
import { registerComponent, type BlockComponentProps } from '../lib/components'
import { AdminLayout } from '../features/admin/AdminLayout'
import { AuditScreen } from '../features/admin/AuditScreen'
import { AutomationsScreen } from '../features/admin/AutomationsScreen'
import { CapabilitiesScreen } from '../features/admin/CapabilitiesScreen'
import { PluginsScreen } from '../features/admin/PluginsScreen'
import { ProjectLayout } from '../features/admin/ProjectLayout'
import {
  ProjectGeneralScreen,
  ProjectPipelinesScreen,
  ProjectPluginsScreen,
  ProjectSyncScreen,
} from '../features/admin/ProjectTabs'
import { ProjectsScreen } from '../features/admin/ProjectsScreen'
import { UsersScreen } from '../features/admin/UsersScreen'
import { QueueScreen } from '../features/queue/QueueScreen'
import { QueueSettingsScreen } from '../features/queue/QueueSettingsScreen'
import {
  ArtifactsBlock,
  ExecutionsBlock,
  ExportsBlock,
  ModuleSectionsBlock,
  NotesBlock,
  PublishBlock,
  RebuildBlock,
  ReleaseHeaderBlock,
  TestsBlock,
} from '../features/releases/ReleaseScreen'
import { ReleasesList } from '../features/releases/ReleasesScreen'
import { EventsScreen } from '../features/runs/EventsScreen'
import { ExecutionsLayout } from '../features/runs/ExecutionsLayout'
import { PlanRunsScreen } from '../features/runs/PlanRunsScreen'
import { RunsScreen } from '../features/runs/RunsScreen'
import { VersionsScreen } from '../features/versions/VersionsScreen'

/*
 * Les composants du noyau, sous les noms que ses écrans décrits en données
 * citent (`internal/ui/resources`).
 */

/*
 * Découpage du bundle sur les deux écrans lourds (doc 08 §8.6).
 *
 * Le choix n'est pas fait au poids des écrans mais à celui de leurs dépendances.
 * `RunScreen` entraîne la visionneuse de logs et son virtualiseur, dont la page
 * d'accueil n'a que faire : la charger avec elle retarde le premier rendu pour
 * tout le monde.
 */
const RunScreen = lazy(() =>
  import('../features/runs/RunScreen').then((module) => ({ default: module.RunScreen })),
)
const VersionScreen = lazy(() =>
  import('../features/versions/VersionScreen').then((module) => ({ default: module.VersionScreen })),
)

function PlaceholderBlock({ title, summary, lot, params }: BlockComponentProps) {
  return (
    <Placeholder
      title={String(title ?? '')}
      summary={String(summary ?? '')}
      lot={String(lot ?? '')}
      params={Array.isArray(params) ? params.map(String) : []}
    />
  )
}

export function registerCoreComponents(): void {
  const core = {
    shell: AppShell,
    'home-redirect': HomeRedirect,
    'not-found': NotFound,
    placeholder: PlaceholderBlock,
    versions: VersionsScreen,
    version: VersionScreen,
    'releases-list': ReleasesList,
    'release-header': ReleaseHeaderBlock,
    'release-rebuild': RebuildBlock,
    'release-publish': PublishBlock,
    'release-tests': TestsBlock,
    'release-artifacts': ArtifactsBlock,
    'release-modules': ModuleSectionsBlock,
    'release-notes': NotesBlock,
    'release-exports': ExportsBlock,
    'release-executions': ExecutionsBlock,
    'executions-layout': ExecutionsLayout,
    runs: RunsScreen,
    run: RunScreen,
    queue: QueueScreen,
    events: EventsScreen,
    'plan-runs': PlanRunsScreen,
    'queue-settings': QueueSettingsScreen,
    'admin-layout': AdminLayout,
    projects: ProjectsScreen,
    plugins: PluginsScreen,
    capabilities: CapabilitiesScreen,
    users: UsersScreen,
    audit: AuditScreen,
    'project-layout': ProjectLayout,
    'project-general': ProjectGeneralScreen,
    'project-plugins': ProjectPluginsScreen,
    'project-pipelines': ProjectPipelinesScreen,
    'project-automations': AutomationsScreen,
    'project-sync': ProjectSyncScreen,
  }
  for (const [name, component] of Object.entries(core)) registerComponent(`core/${name}`, component)
}
