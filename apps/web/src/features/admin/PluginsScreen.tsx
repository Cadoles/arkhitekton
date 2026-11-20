import { Card, EmptyState, ErrorState, Skeleton } from '../../components/primitives'
import { InstallationsCard } from './InstallationsCard'
import { LeftoversCard } from './LeftoversCard'
import { PluginRow } from './PluginCard'
import { usePlugins, useSavePlugin } from './plugins'

/*
 * Les plugins de l'instance.
 *
 * Ceux que le binaire embarque (doc 12 §12.5), et ceux qu'un administrateur a
 * installés depuis un registre. L'écran dit lesquels sont disponibles et porte
 * ce qui vaut pour toute l'instance.
 *
 * Ce qui est propre à un projet — son serveur de suivi, son déploiement — se règle
 * sur sa page. Le partage est celui de doc 12 §12.5 : le global porte les
 * **défauts**, le projet porte ses **écarts**.
 */
export function PluginsScreen() {
  const { data, isPending, error, refetch } = usePlugins()
  const save = useSavePlugin()

  if (isPending) return <Skeleton className="h-64 w-full" />
  if (error) {
    return (
      <ErrorState message="Les plugins n'ont pas pu être chargés." onRetry={() => void refetch()} />
    )
  }
  if (!data) return null

  return (
    <div className="flex flex-col gap-4">
      <InstallationsCard />
      <LeftoversCard />
      <Card title={`Plugins (${data.plugins.length})`}>
        <p className="mb-1 text-sm text-zinc-500">
          Un plugin désactivé ici disparaît des écrans de projet ; ses réglages valent pour toute
          l&apos;instance, et chaque projet peut s&apos;en écarter.
        </p>

        {data.plugins.length === 0 ? (
          <EmptyState>Aucun plugin n'est disponible sur cette instance.</EmptyState>
        ) : (
          <ul className="flex flex-col divide-y divide-zinc-200">
            {data.plugins.map((plugin) => (
              <PluginRow
                key={plugin.id}
                plugin={plugin}
                scope="global"
                saving={save.isPending}
                onSave={(draft) => save.mutate({ id: plugin.id, draft })}
              />
            ))}
          </ul>
        )}

        {save.error && <p className="mt-3 text-sm text-critical-700">{save.error.message}</p>}
      </Card>
    </div>
  )
}
