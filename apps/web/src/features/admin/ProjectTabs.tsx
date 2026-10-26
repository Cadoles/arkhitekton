import { useOutletContext } from 'react-router'
import { Card, EmptyState } from '../../components/primitives'
import { SyncPanel } from './SyncPanel'
import { PluginCard } from './PluginCard'
import { ProjectForm, ProjectKey, toDraft } from './ProjectForm'
import type { Project } from './api'
import {
  useClearProjectPlugin,
  useProjectPipelines,
  useProjectPlugins,
  useSaveProjectPipeline,
  useSaveProjectPlugin,
} from './plugins'

/*
 * Les quatre vues d'un projet.
 *
 * Elles reçoivent le projet par le contexte de la coquille plutôt que de le
 * relire chacune : c'est la même donnée, et quatre lectures indépendantes
 * pourraient montrer quatre états d'un projet qu'on vient de modifier.
 */

function useProjectContext() {
  return useOutletContext<Project>()
}

/** Les paramètres généraux : dépôt, construction, images, schéma de version. */
export function ProjectGeneralScreen() {
  const project = useProjectContext()

  return (
    <div className="flex flex-col gap-4">
      <Card title="Paramètres">
        <ProjectForm id={project.id} draft={toDraft(project)} />
      </Card>

      <Card title="Clé de déploiement">
        {/* Le seul réglage dont l'absence **empêche de construire** : la clé
            partagée qui servait de repli a été retirée, une clé unique ajoutée à
            plusieurs dépôts faisant de sa révocation une panne pour tous. */}
        <ProjectKey project={project} />
      </Card>
    </div>
  )
}

/** Les plugins que ce projet retient, et ce qu'il en règle. */
export function ProjectPluginsScreen() {
  const project = useProjectContext()
  const { data, isPending } = useProjectPlugins(project.slug)
  const save = useSaveProjectPlugin(project.slug)
  const clear = useClearProjectPlugin(project.slug)

  if (isPending || !data) return null

  return (
    <Card title="Plugins de ce projet">
      <p className="mb-3 text-sm text-zinc-500">
        Ce que ce projet retient de ce que l'instance propose. Un champ laissé vide reprend
        la valeur de l'instance — il ne l'efface pas.
      </p>

      {data.plugins.length === 0 ? (
        <EmptyState>
          Aucun plugin ne se règle par projet, ou l'instance les a tous désactivés.
        </EmptyState>
      ) : (
        <ul className="flex flex-col gap-3">
          {data.plugins.map((plugin) => (
            <PluginCard
              key={`${plugin.id}:${String(plugin.enabled)}`}
              plugin={plugin}
              scope="project"
              saving={save.isPending}
              onSave={(draft) => save.mutate({ id: plugin.id, draft })}
              onClear={() => clear.mutate(plugin.id)}
            />
          ))}
        </ul>
      )}

      {save.error && <p className="mt-3 text-sm text-critical-700">{save.error.message}</p>}
    </Card>
  )
}

/**
 * Les familles du registre de pipelines que ce projet vérifie.
 *
 * Le catalogue dit ce que l'instance sait lancer ; il ne dit pas ce que ce projet
 * veut lancer. Un projet sans campagne de charge voyait `load-tests` s'enchaîner
 * après chaque construction, échouer faute de scénario, et laisser sa release en
 * attente d'un verdict qui n'arriverait jamais.
 */
export function ProjectPipelinesScreen() {
  const project = useProjectContext()
  const { data, isPending } = useProjectPipelines(project.slug)
  const save = useSaveProjectPipeline(project.slug)

  if (isPending || !data) return null

  return (
    <Card title="Pipelines de ce projet">
      <p className="mb-3 text-sm text-zinc-500">
        Le registre de l'instance, et ce que ce projet en retient. Une famille de
        vérification retirée cesse de s'enchaîner après une construction, et cesse d'être
        attendue pour conclure une release.
      </p>

      {data.pipelines.length === 0 ? (
        <EmptyState>Le catalogue de cette instance ne déclare aucun pipeline.</EmptyState>
      ) : (
        <ul className="flex flex-col gap-1">
          {data.pipelines.map((pipeline) => (
            <li
              key={pipeline.kind}
              className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border border-zinc-200 px-3 py-2"
            >
              <label className="flex items-center gap-2 text-sm text-zinc-900">
                <input
                  type="checkbox"
                  checked={pipeline.enabled}
                  disabled={save.isPending}
                  onChange={(event) =>
                    save.mutate({ kind: pipeline.kind, enabled: event.target.checked })
                  }
                  className="size-4 rounded border-zinc-300"
                />
                {pipeline.label}
              </label>

              <span className="font-mono text-xs text-zinc-500">{pipeline.kind}</span>

              {pipeline.verification ? (
                <span className="rounded border border-zinc-200 px-1.5 py-0.5 text-xs text-zinc-500">
                  enchaînée après construction
                </span>
              ) : (
                <span className="rounded border border-zinc-200 px-1.5 py-0.5 text-xs text-zinc-500">
                  lancée à la main
                </span>
              )}

              {/* Ce que le projet n'a pas décidé suit le défaut. Le dire évite de
                  prendre un défaut pour un choix, et inversement. */}
              {!pipeline.declared && (
                <span className="text-xs text-zinc-400">par défaut</span>
              )}
            </li>
          ))}
        </ul>
      )}

      {save.error && <p className="mt-3 text-sm text-critical-700">{save.error.message}</p>}
    </Card>
  )
}

/** L'état de la synchronisation du suivi de ce projet. */
export function ProjectSyncScreen() {
  const project = useProjectContext()
  return <SyncPanel project={project.slug} />
}
