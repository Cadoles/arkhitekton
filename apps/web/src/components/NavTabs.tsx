import { useParams } from 'react-router'
import { Icon, iconOr } from './Icon'
import { SubTabs } from './SubTabs'
import { useNavigation } from '../lib/interface'
import { renderTemplate } from '../lib/template'

/** Les onglets d'un écran, lus d'une navigation de l'interface : un plugin peut
 *  y ajouter les siens. Leur adresse peut citer les paramètres de la route
 *  (`{route.slug}`). */
export function NavTabs({ navigation, label }: { navigation: string; label: string }) {
  const items = useNavigation(navigation).items ?? []
  const route = useParams()
  return (
    <SubTabs
      label={label}
      tabs={items.map((item) => ({
        to: renderTemplate(item.to, [{ route }]),
        label: item.label,
        icon: <Icon name={iconOr(item.icon)} className="size-4" />,
        end: item.end,
      }))}
    />
  )
}
