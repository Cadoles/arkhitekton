import { useQueries, useQueryClient, type QueryKey } from '@tanstack/react-query'
import { request } from './api'
import type { DataSource } from './interface'
import { projectPath } from './project'
import { renderTemplate, type Scope } from './template'

/*
 * Les données qu'un écran ou un bloc décrit en données charge, sous le nom que
 * ses gabarits lisent.
 *
 * Une source `core` nomme une lecture du noyau ; elle partage la clé de cache du
 * hook qui la lit déjà ailleurs, et ne se charge donc qu'une fois par écran.
 * Une source `plugin` appelle une route GET que le plugin déclare : le serveur a
 * vérifié la route et rempli le plugin et la portée.
 */

type Resolved = { key: QueryKey; path: string }

const CORE: Record<string, (params: Record<string, string>) => Resolved> = {
  release: ({ tag = '' }) => ({ key: ['releases', tag], path: projectPath(`/releases/${encodeURIComponent(tag)}`) }),
  releases: () => ({ key: ['releases'], path: projectPath('/releases') }),
  version: ({ id = '' }) => ({ key: ['versions', id], path: projectPath(`/versions/${encodeURIComponent(id)}`) }),
  run: ({ name = '' }) => ({ key: ['run', name], path: `/runs/${encodeURIComponent(name)}` }),
}

/** Le chemin d'API d'une route de plugin. */
export function pluginPath(plugin: string, scope: string | undefined, path: string): string {
  const relative = `/plugins/${encodeURIComponent(plugin)}/${path}`
  return scope === 'instance' ? relative : projectPath(relative)
}

function resolve(source: DataSource, scope: Scope): Resolved | undefined {
  if (source.source === 'core') {
    const read = source.name ? CORE[source.name] : undefined
    if (!read) return undefined
    const params = Object.fromEntries(
      Object.entries(source.params ?? {}).map(([name, value]) => [name, renderTemplate(value, scope)]),
    )
    return read(params)
  }
  if (!source.plugin || !source.path) return undefined
  const path = pluginPath(source.plugin, source.scope, renderTemplate(source.path, scope))
  return { key: ['plugin', source.plugin, path], path }
}

export type LoadedData = {
  values: Record<string, unknown>
  pending: boolean
  error: boolean
  retry: () => void
  /** Relit les données nommées, toutes si aucune ne l'est. */
  invalidate: (names?: string[]) => Promise<void>
}

/** Charge les sources, leurs gabarits lus dans `scope` (la route d'abord). */
export function useData(sources: Record<string, DataSource> | undefined, scope: Scope): LoadedData {
  const client = useQueryClient()
  const entries = Object.entries(sources ?? {}).map(([name, source]) => ({ name, resolved: resolve(source, scope) }))
  const results = useQueries({
    queries: entries.map(({ resolved }) => ({
      queryKey: resolved?.key ?? ['ui-source-unknown'],
      queryFn: () => request<unknown>(resolved?.path ?? ''),
      enabled: Boolean(resolved),
    })),
  })
  const values: Record<string, unknown> = {}
  entries.forEach(({ name }, index) => {
    values[name] = results[index]?.data
  })
  return {
    values,
    pending: results.some((result) => result.isPending && result.fetchStatus !== 'idle'),
    error: results.some((result) => result.isError) || entries.some(({ resolved }) => !resolved),
    retry: () => results.forEach((result) => void result.refetch()),
    invalidate: async (names) => {
      const chosen = entries.filter(({ name }) => !names || names.length === 0 || names.includes(name))
      await Promise.all(
        chosen.flatMap(({ resolved }) => (resolved ? [client.invalidateQueries({ queryKey: resolved.key })] : [])),
      )
    },
  }
}
