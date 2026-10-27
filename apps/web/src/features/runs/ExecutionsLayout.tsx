import { Outlet } from 'react-router'
import { NavTabs } from '../../components/NavTabs'

/*
 * Coquille des trois vues d'exécution.
 *
 * Elles répondaient à la même question depuis deux entrées de menu : « qu'est-ce
 * qui tourne, et pourquoi ça n'avance pas ». Séparées, il fallait naviguer de
 * l'une à l'autre pour rapprocher un run de la file qui l'a fait attendre — et
 * les réglages de file n'avaient nulle part où vivre.
 *
 * Les sous-onglets les remettent ensemble sans les mélanger : l'historique des
 * exécutions, l'état des files, les faits constatés, ce qu'ils ont déclenché, et
 * ce qui gouverne l'ensemble.
 *
 * Les faits y ont leur place pour la même raison que le reste : ce sont trois
 * façons de regarder ce que le dashboard a fait, et les séparer obligerait à
 * naviguer pour rapprocher un fait du run qui l'a produit.
 */


export function ExecutionsLayout() {
  return (
    <div className="flex flex-col gap-4">
      <NavTabs navigation="executions" label="Vues d'exécution" />
      <Outlet />
    </div>
  )
}
