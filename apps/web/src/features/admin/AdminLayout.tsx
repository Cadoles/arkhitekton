import { Outlet } from 'react-router'
import { Icon } from '../../components/Icon'
import { RedmineMark } from '../../components/RedmineMark'
import { SubTabs, type SubTab } from '../../components/SubTabs'

/*
 * Coquille de l'administration.
 *
 * Les trois sujets vivaient empilés sur une même page : la synchronisation
 * Redmine, les rôles, le journal d'audit. Ils n'ont ni la même fréquence de
 * consultation ni le même lecteur — on vient vérifier une synchronisation en
 * passant, on attribue un rôle rarement, on lit le journal après coup — et la
 * page se parcourait au lieu de se lire.
 *
 * Le journal reste à un clic des rôles, et c'est ce qui compte : il sert d'abord
 * à contrôler les attributions.
 */

const TABS: SubTab[] = [
  { to: '/admin', label: 'Redmine', icon: <RedmineMark className="size-4" />, end: true },
  { to: '/admin/users', label: 'Utilisateurs', icon: <Icon name="user" className="size-4" /> },
  { to: '/admin/logs', label: "Journal d'audit", icon: <Icon name="doc" className="size-4" /> },
]

export function AdminLayout() {
  return (
    <div className="flex flex-col gap-4">
      <h1 className="text-2xl font-semibold text-zinc-900">Administration</h1>
      <SubTabs tabs={TABS} label="Vues d'administration" />
      <Outlet />
    </div>
  )
}
