import { lazy } from 'react'
import { Navigate, type RouteObject } from 'react-router'
import { AppShell } from './components/AppShell'
import { Placeholder } from './components/Placeholder'
import { NotFound } from './components/NotFound'
import { HomeRedirect, ModuleRoute } from './components/ModuleRoute'
import { appModules } from './modules'
import { RunsScreen } from './features/runs/RunsScreen'
import { ExecutionsLayout } from './features/runs/ExecutionsLayout'
import { QueueScreen } from './features/queue/QueueScreen'
import { QueueSettingsScreen } from './features/queue/QueueSettingsScreen'
import { EventsScreen } from './features/runs/EventsScreen'
import { PlanRunsScreen } from './features/runs/PlanRunsScreen'
import { AutomationsScreen } from './features/admin/AutomationsScreen'
import { ReleasesScreen } from './features/releases/ReleasesScreen'
import { ReleaseScreen } from './features/releases/ReleaseScreen'
import { AdminLayout } from './features/admin/AdminLayout'
import { UsersScreen } from './features/admin/UsersScreen'
import { ProjectsScreen } from './features/admin/ProjectsScreen'
import { ProjectLayout } from './features/admin/ProjectLayout'
import {
  ProjectGeneralScreen,
  ProjectPipelinesScreen,
  ProjectPluginsScreen,
  ProjectSyncScreen,
} from './features/admin/ProjectTabs'
import { CapabilitiesScreen } from './features/admin/CapabilitiesScreen'
import { PluginsScreen } from './features/admin/PluginsScreen'
import { AuditScreen } from './features/admin/AuditScreen'
import { VersionsScreen } from './features/versions/VersionsScreen'

/*
 * Arborescence de la doc 08 §8.3, posée en entier dès le lot 0.
 *
 * Déclarer les routes avant les écrans n'est pas de l'anticipation gratuite :
 * c'est ce qui permet de vérifier la coquille, la navigation et les paramètres
 * d'URL en conditions réelles, et de constater tout de suite qu'un lien de menu
 * mène quelque part.
 */

/*
 * Découpage du bundle sur les deux écrans lourds (doc 08 §8.6).
 *
 * Le choix n'est pas fait au poids des écrans mais à celui de leurs dépendances.
 * `RunScreen` entraîne la visionneuse de logs et son virtualiseur, dont la page
 * d'accueil — `/environments` — n'a que faire : la charger avec elle retarde le
 * premier rendu pour tout le monde.
 *
 * Le détail des tests n'a plus d'écran à découper : il s'ouvre en modale depuis
 * la grille des signaux, donc dans le même bundle que l'écran d'une release.
 *
 * Les autres écrans restent en import direct : découper une carte de deux cents
 * lignes échange un gain nul contre un aller-retour réseau.
 */
const RunScreen = lazy(() =>
  import('./features/runs/RunScreen').then((module) => ({ default: module.RunScreen })),
)
const VersionScreen = lazy(() =>
  import('./features/versions/VersionScreen').then((module) => ({ default: module.VersionScreen })),
)

/** Les routes, avec celles des modules connus à cet instant : appelée une fois
 *  les interfaces des plugins chargées. */
export function buildRoutes(): RouteObject[] {
  return [
    {
      path: '/',
      element: <AppShell />,
      children: [
        { index: true, element: <HomeRedirect /> },

        // Les écrans des modules, chacun gardé par son interrupteur : éteint, une
        // URL qui y mène renvoie à l'accueil.
        ...appModules().flatMap((module) =>
          (module.routes ?? []).map((route) => ({
            ...route,
            element: <ModuleRoute module={module.id}>{route.element}</ModuleRoute>,
          })),
        ),

        { path: 'versions', element: <VersionsScreen /> },
        { path: 'versions/:id', element: <VersionScreen /> },

        { path: 'releases', element: <ReleasesScreen /> },
        { path: 'releases/:tag', element: <ReleaseScreen /> },
        {
          path: 'releases/:tag/notes',
          element: (
            <Placeholder
              lot="lot 6"
              title="Note de release"
              summary="Note générée puis éditable, avec la date et l'auteur de la dernière modification."
              params={['tag']}
            />
          ),
        },

        /*
         * Les trois vues d'exécution sous une coquille commune. Elles répondaient
         * à la même question depuis deux entrées de menu : ce qui tourne, ce qui
         * attend, et ce qui gouverne l'attente.
         */
        {
          path: 'runs',
          element: <ExecutionsLayout />,
          children: [
            { index: true, element: <RunsScreen /> },
            { path: 'queue', element: <QueueScreen /> },
            { path: 'events', element: <EventsScreen /> },
            { path: 'automations', element: <PlanRunsScreen /> },
            { path: 'settings', element: <QueueSettingsScreen /> },
          ],
        },
        // Hors coquille : le détail d'une exécution n'est pas une quatrième vue, et
        // lui coiffer des onglets ferait croire qu'on peut y revenir. Le segment
        // statique l'emporte sur le paramètre, donc `/runs/queue` ne cherche pas
        // une exécution de ce nom.
        { path: 'runs/:name', element: <RunScreen /> },

        // Ancienne adresse de la file, conservée : les liens partagés et les
        // signets ne se corrigent pas d'eux-mêmes.
        { path: 'queue', element: <Navigate to="/runs/queue" replace /> },

        /*
         * L'administration, sous une coquille commune : les projets, les plugins de
         * l'instance, les rôles, le journal d'audit. Ces sujets n'ont ni la même
         * fréquence de consultation ni le même lecteur, et empilés ils faisaient une
         * page qu'on parcourait au lieu de la lire.
         *
         * Chaque projet a sa page, avec ses propres sous-onglets : ses paramètres,
         * ses plugins, ses pipelines, l'état de son suivi. C'est la conséquence de
         * ce que les plugins servent d'abord les spécificités d'un projet — les
         * régler ailleurs qu'auprès de lui ferait chercher au mauvais endroit.
         */
        {
          path: 'admin',
          element: <AdminLayout />,
          children: [
            { index: true, element: <ProjectsScreen /> },
            { path: 'plugins', element: <PluginsScreen /> },
            { path: 'capabilities', element: <CapabilitiesScreen /> },
            { path: 'users', element: <UsersScreen /> },
            // Conservée : l'onglet Projets était à cette adresse, et les liens
            // partagés ne se corrigent pas d'eux-mêmes.
            { path: 'projects', element: <ProjectsScreen /> },
            {
              path: 'projects/:slug',
              element: <ProjectLayout />,
              children: [
                { index: true, element: <ProjectGeneralScreen /> },
                { path: 'plugins', element: <ProjectPluginsScreen /> },
                { path: 'pipelines', element: <ProjectPipelinesScreen /> },
                { path: 'automations', element: <AutomationsScreen /> },
                { path: 'sync', element: <ProjectSyncScreen /> },
              ],
            },
            { path: 'logs', element: <AuditScreen /> },
          ],
        },

        { path: '*', element: <NotFound /> },
      ],
    },
  ]
}
