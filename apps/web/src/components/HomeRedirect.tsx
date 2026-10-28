import { Navigate } from 'react-router'
import { useNavigation } from '../lib/interface'

/**
 * La page d'accueil : celle que le menu principal de l'interface désigne, qu'un
 * plugin peut fixer (ses environnements par exemple), les releases sinon. C'est
 * ce que le noyau sait montrer sans plugin.
 */
export function HomeRedirect() {
  return <Navigate to={useNavigation('main').home ?? '/releases'} replace />
}
