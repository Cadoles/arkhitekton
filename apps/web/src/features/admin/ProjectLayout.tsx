import { Link, Outlet, useParams } from 'react-router'
import { NavTabs } from '../../components/NavTabs'
import { ErrorState, Skeleton } from '../../components/primitives'
import { useProjects, type Project } from './api'

/*
 * La page d'un projet.
 *
 * Le réglage d'un projet vivait dans une ligne dépliée de la liste. Il n'y tient
 * plus : à ses paramètres généraux se sont ajoutés ses plugins, ses pipelines,
 * ses automatisations et l'état de son suivi — cinq sujets qui n'ont ni la même
 * fréquence de consultation ni le même lecteur, et qui empilés faisaient un
 * formulaire qu'on parcourait au lieu de le lire.
 *
 * Chacun a donc son adresse. Ces adresses finissent en favori, et c'est le slug
 * qui les porte plutôt que l'identifiant : il est stable, lisible, et déjà celui
 * des autres écrans.
 */

export function ProjectLayout() {
  const { slug = '' } = useParams()
  const project = useProject(slug)

  if (project.isPending) return <Skeleton className="h-64 w-full" />
  if (project.error) {
    return (
      <ErrorState
        message="Le projet n'a pas pu être chargé."
        onRetry={() => void project.refetch()}
      />
    )
  }
  if (!project.data) {
    // Un slug corrigé depuis cet écran laisse des favoris périmés : le dire vaut
    // mieux qu'une page vide, et le retour à la liste évite l'impasse.
    return (
      <div className="flex flex-col items-start gap-3">
        <ErrorState message={`Aucun projet ne porte le nom « ${slug} ».`} />
        <Link to="/admin/projects" className="text-sm text-zinc-700 underline">
          Revenir à la liste
        </Link>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <Link to="/admin/projects" className="text-sm text-zinc-500 hover:underline">
          Dépôts pilotés
        </Link>
        <span className="text-zinc-300">/</span>
        <h2 className="text-lg font-semibold text-zinc-900">{project.data.label}</h2>
        <span className="font-mono text-xs text-zinc-500">{project.data.repository}</span>
      </div>

      <NavTabs navigation="project" label={`Réglages de ${project.data.label}`} />
      <Outlet context={project.data} />
    </div>
  )
}

/**
 * Le projet nommé par l'URL, tiré de la liste.
 *
 * Depuis la liste et non d'un appel dédié : l'API rend les projets en bloc, et
 * l'écran de liste l'a déjà en cache — un second point d'entrée pour une donnée
 * déjà là ajouterait une route à tenir pour rien.
 */
function useProject(slug: string) {
  const projects = useProjects()
  return {
    ...projects,
    data: projects.data?.projects.find((entry: Project) => entry.slug === slug),
  }
}
