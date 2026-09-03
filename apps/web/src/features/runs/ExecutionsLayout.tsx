import { Outlet } from 'react-router'
import { Icon } from '../../components/Icon'
import { SubTabs, type SubTab } from '../../components/SubTabs'

/*
 * Coquille des trois vues d'exécution.
 *
 * Elles répondaient à la même question depuis deux entrées de menu : « qu'est-ce
 * qui tourne, et pourquoi ça n'avance pas ». Séparées, il fallait naviguer de
 * l'une à l'autre pour rapprocher un run de la file qui l'a fait attendre — et
 * les réglages de file n'avaient nulle part où vivre.
 *
 * Les sous-onglets les remettent ensemble sans les mélanger : l'historique des
 * exécutions, l'état des files, les faits constatés, et ce qui les gouverne.
 *
 * Les faits y ont leur place pour la même raison que le reste : ce sont trois
 * façons de regarder ce que le dashboard a fait, et les séparer obligerait à
 * naviguer pour rapprocher un fait du run qui l'a produit.
 */

const TABS: SubTab[] = [
  { to: '/runs', label: 'Exécutions', icon: <Icon name="beaker" className="size-4" />, end: true },
  { to: '/runs/queue', label: "File d'attente", icon: <Icon name="queue" className="size-4" /> },
  { to: '/runs/events', label: 'Faits', icon: <Icon name="doc" className="size-4" /> },
  { to: '/runs/settings', label: 'Paramètres', icon: <Icon name="cog" className="size-4" /> },
]

export function ExecutionsLayout() {
  return (
    <div className="flex flex-col gap-4">
      <SubTabs tabs={TABS} label="Vues d'exécution" />
      <Outlet />
    </div>
  )
}
