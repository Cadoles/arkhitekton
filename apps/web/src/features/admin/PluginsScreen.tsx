import { Card, EmptyState, ErrorState, Skeleton } from '../../components/primitives'
import { InstallationsCard } from './InstallationsCard'
import { LeftoversCard } from './LeftoversCard'
import { PluginCard } from './PluginCard'
import { usePlugins, useSavePlugin } from './plugins'

/*
 * Les plugins de l'instance.
 *
 * Ceux que le binaire embarque (doc 12 §12.5), et ceux qu'un administrateur a
 * installés depuis un registre. L'écran dit lesquels sont disponibles et porte
 * ce qui vaut pour toute l'instance.
 *
 * Ce qui est propre à un projet — son serveur de suivi, ses livrables — se règle
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
        <p className="mb-3 text-sm text-zinc-500">
          Ce que cette version du dashboard sait faire. Un plugin retiré ici disparaît des écrans de
          projet : un interrupteur qu'on ne peut pas atteindre vaut mieux qu'un réglage qui ne fait
          rien.
        </p>

        {data.plugins.length === 0 ? (
          <EmptyState>Aucun plugin n'est disponible sur cette instance.</EmptyState>
        ) : (
          <ul className="flex flex-col gap-3">
            {data.plugins.map((plugin) => (
              <PluginCard
                // L'état d'activation entre dans la clé : un enregistrement remonte
                // la liste, et sans cela la carte conserverait son état local,
                // affichant une valeur que le serveur a remplacée.
                key={`${plugin.id}:${String(plugin.enabled)}`}
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
