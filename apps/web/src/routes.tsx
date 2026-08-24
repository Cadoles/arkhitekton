import { lazy } from 'react'
import { Navigate, type RouteObject } from 'react-router'
import { AppShell } from './components/AppShell'
import { Placeholder } from './components/Placeholder'
import { NotFound } from './components/NotFound'
import { RunsScreen } from './features/runs/RunsScreen'
import { ExecutionsLayout } from './features/runs/ExecutionsLayout'
import { QueueScreen } from './features/queue/QueueScreen'
import { QueueSettingsScreen } from './features/queue/QueueSettingsScreen'
import { EnvironmentsScreen } from './features/environments/EnvironmentsScreen'
import { HistoryScreen } from './features/environments/HistoryScreen'
import { EnvironmentScreen } from './features/environments/EnvironmentScreen'
import { ReleasesScreen } from './features/releases/ReleasesScreen'
import { ReleaseScreen } from './features/releases/ReleaseScreen'
import { AdminLayout } from './features/admin/AdminLayout'
import { SyncPanel } from './features/admin/SyncPanel'
import { UsersScreen } from './features/admin/UsersScreen'
import { ProjectsScreen } from './features/admin/ProjectsScreen'
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

export const routes: RouteObject[] = [
  {
    path: '/',
    element: <AppShell />,
    children: [
      { index: true, element: <Navigate to="/environments" replace /> },

      { path: 'environments', element: <EnvironmentsScreen /> },
      { path: 'environments/:env', element: <EnvironmentScreen /> },
      { path: 'environments/:env/history', element: <HistoryScreen /> },

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
       * Trois sujets sous une coquille commune : la synchronisation Redmine, les
       * rôles, le journal d'audit. Ils n'ont ni la même fréquence de consultation
       * ni le même lecteur, et empilés ils faisaient une page qu'on parcourait au
       * lieu de la lire.
       */
      {
        path: 'admin',
        element: <AdminLayout />,
        children: [
          { index: true, element: <SyncPanel /> },
          { path: 'users', element: <UsersScreen /> },
          { path: 'projects', element: <ProjectsScreen /> },
          { path: 'logs', element: <AuditScreen /> },
        ],
      },

      { path: '*', element: <NotFound /> },
    ],
  },
]
