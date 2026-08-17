import { lazy } from 'react'
import { Navigate, type RouteObject } from 'react-router'
import { AppShell } from './components/AppShell'
import { Placeholder } from './components/Placeholder'
import { NotFound } from './components/NotFound'
import { RunsScreen } from './features/runs/RunsScreen'
import { QueueScreen } from './features/queue/QueueScreen'
import { EnvironmentsScreen } from './features/environments/EnvironmentsScreen'
import { HistoryScreen } from './features/environments/HistoryScreen'
import { EnvironmentScreen } from './features/environments/EnvironmentScreen'
import { ReleasesScreen } from './features/releases/ReleasesScreen'
import { ReleaseScreen } from './features/releases/ReleaseScreen'
import { SecurityScreen } from './features/releases/SecurityScreen'
import { AdminScreen } from './features/admin/AdminScreen'
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
 * `RunScreen` entraîne la visionneuse de logs et son virtualiseur ; `FamilyScreen`
 * entraîne les tables de cas de test. Ces deux-là ne servent qu'à qui les ouvre,
 * alors que la page d'accueil est `/environments` — la charger avec un virtualiseur
 * dont elle n'a que faire retarde le premier rendu pour tout le monde.
 *
 * Les autres écrans restent en import direct : découper une carte de deux cents
 * lignes échange un gain nul contre un aller-retour réseau.
 */
const RunScreen = lazy(() =>
  import('./features/runs/RunScreen').then((module) => ({ default: module.RunScreen })),
)
const FamilyScreen = lazy(() =>
  import('./features/releases/FamilyScreen').then((module) => ({ default: module.FamilyScreen })),
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
      { path: 'releases/:tag/tests/:family', element: <FamilyScreen /> },
      { path: 'releases/:tag/security', element: <SecurityScreen /> },
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

      { path: 'runs', element: <RunsScreen /> },
      { path: 'runs/:name', element: <RunScreen /> },

      { path: 'queue', element: <QueueScreen /> },

      { path: 'admin', element: <AdminScreen /> },

      { path: '*', element: <NotFound /> },
    ],
  },
]
