import { Outlet } from 'react-router'
import { NavTabs } from '../../components/NavTabs'

/*
 * Coquille de l'administration.
 *
 * Les sujets vivaient empilés sur une même page. Ils n'ont ni la même fréquence
 * de consultation ni le même lecteur — on attribue un rôle rarement, on lit le
 * journal après coup — et la page se parcourait au lieu de se lire.
 *
 * Le journal reste à un clic des rôles, et c'est ce qui compte : il sert d'abord
 * à contrôler les attributions.
 *
 * # Ce qui a quitté cette coquille
 *
 * L'onglet Redmine. Tout ce qu'il montrait appartient à un projet — son curseur,
 * sa projection des statuts, ses versions ambiguës — et le vocabulaire projeté,
 * « En revue », appartient au client et non à l'instance. Il vit désormais sur la
 * page du projet, à côté du plugin qui le configure.
 *
 * # Ce qui l'a rejointe
 *
 * L'onglet Plugins, qui porte ce que **cette version** du dashboard sait faire et
 * ce qui vaut pour toute l'instance. Ce qui est propre à un projet se règle sur sa
 * page : le global porte les défauts, le projet porte ses écarts.
 *
 * L'onglet Capacités, où se lient les pipelines que le fichier de catalogue ne lie
 * pas : ceux d'un plugin installé depuis un registre d'abord.
 *
 * Les onglets sont ceux de la navigation `admin` de l'interface : un plugin peut
 * y ajouter les siens.
 */


export function AdminLayout() {
  return (
    <div className="flex flex-col gap-4">
      <h1 className="text-2xl font-semibold text-zinc-900">Administration</h1>
      <NavTabs navigation="admin" label="Vues d'administration" />
      <Outlet />
    </div>
  )
}
