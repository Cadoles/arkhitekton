import { Icon } from '../../components/Icon'
import type { Release } from '../releases/api'
import { useEnvironments } from './api'

/**
 * Les environnements où la release tourne.
 *
 * Le libellé vient du catalogue : `prod` seul se lit mal dans une liste, et
 * l'écran des environnements affiche « Production ». Le repli sur l'identifiant
 * couvre un environnement retiré du catalogue mais encore observé.
 */
export function DeployedBadges({ release }: { release: Release }) {
  const known = useEnvironments()
  const environments = release.deployed_on ?? []

  if (environments.length === 0) return null

  const label = (id: string) =>
    known.data?.environments.find((entry) => entry.id === id)?.label ?? id

  return (
    <>
      {environments.map((id) => (
        <span
          key={id}
          title={`Déployée sur ${label(id)}`}
          className="inline-flex items-center gap-1 rounded-md border border-zinc-300 bg-zinc-100 px-2 py-0.5 text-xs font-medium text-zinc-700"
        >
          <Icon name="layers" className="size-3.5" />
          {label(id)}
        </span>
      ))}
    </>
  )
}
