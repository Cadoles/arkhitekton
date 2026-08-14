import { Navigate, type RouteObject } from 'react-router'
import { AppShell } from './components/AppShell'
import { Placeholder } from './components/Placeholder'
import { NotFound } from './components/NotFound'
import { RunsScreen } from './features/runs/RunsScreen'
import { RunScreen } from './features/runs/RunScreen'
import { QueueScreen } from './features/queue/QueueScreen'
import { EnvironmentsScreen } from './features/environments/EnvironmentsScreen'
import { HistoryScreen } from './features/environments/HistoryScreen'
import { ReleasesScreen } from './features/releases/ReleasesScreen'
import { ReleaseScreen } from './features/releases/ReleaseScreen'

/*
 * Arborescence de la doc 08 §8.3, posée en entier dès le lot 0.
 *
 * Déclarer les routes avant les écrans n'est pas de l'anticipation gratuite :
 * c'est ce qui permet de vérifier la coquille, la navigation et les paramètres
 * d'URL en conditions réelles, et de constater tout de suite qu'un lien de menu
 * mène quelque part.
 */

export const routes: RouteObject[] = [
  {
    path: '/',
    element: <AppShell />,
    children: [
      { index: true, element: <Navigate to="/environments" replace /> },

      { path: 'environments', element: <EnvironmentsScreen /> },
      {
        path: 'environments/:env',
        element: (
          <Placeholder
            lot="lot 5"
            title="Environnement"
            summary="Vue détaillée reprise de la maquette : score de qualité, pipelines, benchs, sécurité, note de release, anomalies, validations."
            params={['env']}
          />
        ),
      },
      { path: 'environments/:env/history', element: <HistoryScreen /> },

      {
        path: 'versions',
        element: (
          <Placeholder
            lot="lot 1"
            title="Versions"
            summary="Versions déclarées dans Redmine, avec leur échéance et leur niveau de complétion."
          />
        ),
      },
      {
        path: 'versions/:id',
        element: (
          <Placeholder
            lot="lot 1"
            title="Périmètre d'une version"
            summary="Demandes réparties en trois colonnes (à faire, en cours, terminé), anomalies ouvertes comptées à part, releases produites."
            params={['id']}
          />
        ),
      },

      { path: 'releases', element: <ReleasesScreen /> },
      { path: 'releases/:tag', element: <ReleaseScreen /> },
      {
        path: 'releases/:tag/tests/:family',
        element: (
          <Placeholder
            lot="lot 4"
            title="Détail des tests"
            summary="Cas de test filtrables, et surtout comparaison avec la release précédente — nouveaux échecs, échecs résolus, tests ajoutés."
            params={['tag', 'family']}
          />
        ),
      },
      {
        path: 'releases/:tag/security',
        element: (
          <Placeholder
            lot="lot 4"
            title="Sécurité"
            summary="Findings normalisés, filtrables par sévérité, catégorie et statut."
            params={['tag']}
          />
        ),
      },
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

      {
        path: 'admin',
        element: (
          <Placeholder
            lot="lot 0"
            title="Administration"
            summary="Attribution des rôles, configuration des environnements, resynchronisations, journal d'audit."
          />
        ),
      },

      { path: '*', element: <NotFound /> },
    ],
  },
]
