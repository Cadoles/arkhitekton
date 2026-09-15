import type { AppModule } from '../../lib/modules'
import { DeployedBadges } from './DeployedBadges'
import { Deployment } from './Deployment'
import { EnvironmentScreen } from './EnvironmentScreen'
import { EnvironmentsScreen } from './EnvironmentsScreen'
import { HistoryScreen } from './HistoryScreen'

/*
 * Le plugin MSE, vu de l'interface : les environnements, la promotion et les
 * validations de recette (doc 16).
 */

export const MODULE_MSE = 'mse'

export const mseModule: AppModule = {
  id: MODULE_MSE,
  // La question qu'on vient poser quand il est allumé : qu'est-ce qui tourne où.
  home: '/environments',
  nav: [{ to: '/environments', label: 'Environnements', icon: 'layers' }],
  routes: [
    { path: 'environments', element: <EnvironmentsScreen /> },
    { path: 'environments/:env', element: <EnvironmentScreen /> },
    { path: 'environments/:env/history', element: <HistoryScreen /> },
  ],
  releaseSections: [Deployment],
  releaseBadges: [DeployedBadges],
}
