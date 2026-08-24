import { useState } from 'react'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Icon } from '../../components/Icon'
import { Card, EmptyState, ErrorState, Skeleton } from '../../components/primitives'
import { copyText } from '../../lib/clipboard'
import { formatDateTime } from '../../lib/format'
import {
  useDeleteProject,
  useProbeProject,
  useProjects,
  useSaveProject,
  type Project,
  type ProjectDraft,
  type ProjectTarget,
} from './api'

/*
 * Les dépôts que le dashboard pilote.
 *
 * Ils vivaient dans la configuration : en déclarer un de plus demandait de
 * modifier un ConfigMap, donc de reconstruire et de redéployer. Rien ne disait,
 * depuis l'interface, quels dépôts le dashboard croyait piloter — et surtout,
 * rien ne permettait de vérifier qu'il y avait accès. L'écart ne se découvrait
 * qu'au clone, en cours de pipeline, sur un message qui ne le désignait pas.
 *
 * L'écran décrit ; il ne pilote pas encore. Les pipelines continuent de recevoir
 * leurs dépôts par configuration, et le raccordement demandera de trancher ce que
 * l'ordonnanceur accepte d'une demande.
 */

const EMPTY: ProjectDraft = {
  repository: '',
  label: '',
  forge_url: '',
  dockerfile: '',
  build_context: '.',
  multi_stage: false,
  targets: [],
}

export function ProjectsScreen() {
  const { data, isPending, error, refetch } = useProjects()
  const [editing, setEditing] = useState<Project | null>(null)
  const [adding, setAdding] = useState(false)

  return (
    <div className="flex flex-col gap-4">
      {isPending && <Skeleton className="h-64 w-full" />}
      {error && (
        <ErrorState message="Les projets n'ont pas pu être chargés." onRetry={() => void refetch()} />
      )}

      {data && (
        <>
          <DeployKeyCard deployKey={data.deploy_key} />

          <Card
            title={`Dépôts pilotés (${data.projects.length})`}
            action={
              !adding &&
              editing === null && (
                <button
                  type="button"
                  onClick={() => setAdding(true)}
                  className="rounded-md border border-zinc-900 bg-zinc-900 px-2.5 py-1 text-xs font-medium text-white transition-colors hover:bg-zinc-700"
                >
                  Déclarer un dépôt
                </button>
              )
            }
          >
            {adding && (
              <div className="mb-4">
                <ProjectForm
                  draft={{ ...EMPTY, forge_url: data.forge_url }}
                  onDone={() => setAdding(false)}
                />
              </div>
            )}

            {data.projects.length === 0 && !adding ? (
              <EmptyState>
                Aucun dépôt déclaré. Le dashboard emploie encore ceux de sa configuration.
              </EmptyState>
            ) : (
              <ul className="flex flex-col gap-2">
                {data.projects.map((project) =>
                  editing?.id === project.id ? (
                    <li key={project.id} className="rounded-md border border-zinc-300 p-3">
                      <ProjectForm
                        id={project.id}
                        draft={toDraft(project)}
                        onDone={() => setEditing(null)}
                      />
                    </li>
                  ) : (
                    <ProjectRow
                      key={project.id}
                      project={project}
                      probeConfigured={data.probe_configured}
                      onEdit={() => setEditing(project)}
                    />
                  ),
                )}
              </ul>
            )}
          </Card>
        </>
      )}
    </div>
  )
}

function toDraft(project: Project): ProjectDraft {
  return {
    repository: project.repository,
    label: project.label,
    forge_url: project.forge_url,
    dockerfile: project.dockerfile,
    build_context: project.build_context,
    multi_stage: project.multi_stage,
    targets: project.targets,
  }
}

/**
 * La clé de déploiement, à coller dans les réglages de chaque dépôt.
 *
 * C'est la moitié **publique** de celle que les pipelines présentent au clone :
 * l'afficher n'expose rien. Une seule clé pour tous les dépôts, et non une par
 * projet — en générer demanderait au dashboard le droit d'écrire des secrets,
 * qu'il n'a pas et ne doit pas avoir.
 */
function DeployKeyCard({ deployKey }: { deployKey: string }) {
  const [state, setState] = useState<'idle' | 'copied' | 'failed'>('idle')

  if (!deployKey) {
    return (
      <Card title="Clé de déploiement">
        <EmptyState>
          La clé publique n'est pas configurée. Renseignez{' '}
          <span className="font-mono">PIPELINE_GIT_PUBLIC_KEY</span> avec la moitié publique de la
          clé montée dans les pipelines pour pouvoir la copier d'ici.
        </EmptyState>
      </Card>
    )
  }

  return (
    <Card title="Clé de déploiement">
      <p className="mb-3 text-sm text-zinc-600">
        À ajouter dans les clés de déploiement de chaque dépôt piloté — en lecture seule : les
        pipelines clonent, ils ne poussent pas. La même clé sert pour tous.
      </p>

      <div className="flex items-start gap-2">
        <code className="min-w-0 flex-1 break-all rounded-md border border-zinc-200 bg-zinc-100 px-2 py-1.5 font-mono text-xs text-zinc-900">
          {deployKey}
        </code>
        <button
          type="button"
          onClick={() => {
            void copyText(deployKey).then((done) => setState(done ? 'copied' : 'failed'))
          }}
          className="flex shrink-0 items-center gap-1 rounded-md border border-zinc-300 bg-white px-2 py-1.5 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
        >
          <Icon name={state === 'copied' ? 'check' : 'link'} className="size-3.5" />
          {state === 'copied' ? 'Copiée' : 'Copier'}
        </button>
      </div>

      {state === 'failed' && (
        <p className="mt-2 text-xs text-critical-700">
          Le navigateur a refusé la copie — sélectionnez le texte à la main.
        </p>
      )}
    </Card>
  )
}

function ProjectRow({
  project,
  probeConfigured,
  onEdit,
}: {
  project: Project
  probeConfigured: boolean
  onEdit: () => void
}) {
  const probe = useProbeProject()
  const remove = useDeleteProject()
  const [confirming, setConfirming] = useState(false)

  return (
    <li className="flex flex-col gap-2 rounded-md border border-zinc-200 px-3 py-2">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="font-medium text-zinc-900">{project.label}</span>

        {project.url ? (
          <a
            href={project.url}
            target="_blank"
            rel="noreferrer noopener"
            className="font-mono text-xs text-zinc-600 hover:underline"
          >
            {project.repository}
          </a>
        ) : (
          <span className="font-mono text-xs text-zinc-600">{project.repository}</span>
        )}

        {project.dockerfile ? (
          <span className="rounded border border-zinc-200 px-1.5 py-0.5 text-xs text-zinc-500">
            {project.targets.length > 0
              ? `${project.targets.length} étape(s)`
              : 'image unique'}
          </span>
        ) : (
          // Un dépôt cloné sans être construit : c'est le cas de `mse-kustom` et
          // de `mse-tekton`, que l'audit lit sans jamais en tirer d'image.
          <span className="rounded border border-zinc-200 px-1.5 py-0.5 text-xs text-zinc-500">
            sans construction
          </span>
        )}

        <AccessBadge project={project} />

        <div className="ml-auto flex items-center gap-2">
          <button
            type="button"
            disabled={!probeConfigured || probe.isPending}
            onClick={() => probe.mutate(project.id)}
            title={
              probeConfigured
                ? "Vérifie que le dashboard lit ce dépôt par l'API Gitea"
                : "La forge n'est pas configurée : le test ne peut pas conclure"
            }
            className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-50"
          >
            {probe.isPending ? 'Test…' : "Tester l'accès"}
          </button>
          <button
            type="button"
            onClick={onEdit}
            className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
          >
            Modifier
          </button>
          <button
            type="button"
            onClick={() => setConfirming(true)}
            className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
          >
            Retirer
          </button>
        </div>
      </div>

      {project.dockerfile && (
        <p className="text-xs text-zinc-500">
          <span className="font-mono">{project.dockerfile}</span>
          {project.build_context !== '.' && (
            <>
              {' '}
              — contexte <span className="font-mono">{project.build_context}</span>
            </>
          )}
          {project.targets.length > 0 && (
            <>
              {' — '}
              {project.targets.map((target) => `${target.stage} → ${target.image}`).join(', ')}
            </>
          )}
        </p>
      )}

      {project.check_note && (
        <p className={`text-xs ${project.check_ok ? 'text-zinc-500' : 'text-critical-700'}`}>
          {project.check_note}
        </p>
      )}

      {confirming && (
        <ConfirmDialog
          title="Retirer ce dépôt ?"
          confirmLabel="Retirer"
          pending={remove.isPending}
          onCancel={() => setConfirming(false)}
          onConfirm={() => {
            remove.mutate(project.id)
            setConfirming(false)
          }}
        >
          <p>
            <span className="font-mono text-zinc-900">{project.repository}</span> disparaîtra de la
            liste.
          </p>
          <p className="text-zinc-500">
            Rien n'est supprimé dans la forge, et les pipelines qui le clonent par configuration
            continueront de le faire : cette liste décrit, elle ne pilote pas encore.
          </p>
        </ConfirmDialog>
      )}
    </li>
  )
}

/**
 * L'état d'accès constaté.
 *
 * Trois états et non deux : jamais testé n'est pas la même chose qu'un test qui a
 * échoué, et les confondre ferait chercher un défaut de droits là où personne n'a
 * encore regardé.
 */
function AccessBadge({ project }: { project: Project }) {
  if (project.checked_at === undefined) {
    return <span className="text-xs text-zinc-400">jamais testé</span>
  }

  const tone = project.check_ok
    ? 'border-healthy-500 bg-healthy-50 text-healthy-700'
    : 'border-critical-500 bg-critical-50 text-critical-700'

  return (
    <span
      className={`rounded border px-1.5 py-0.5 text-xs ${tone}`}
      title={`Testé le ${formatDateTime(project.checked_at)}`}
    >
      {project.check_ok ? 'accès vérifié' : 'accès refusé'}
    </span>
  )
}

function ProjectForm({
  id,
  draft,
  onDone,
}: {
  id?: number
  draft: ProjectDraft
  onDone: () => void
}) {
  const [form, setForm] = useState<ProjectDraft>(draft)
  const save = useSaveProject()

  const set = <K extends keyof ProjectDraft>(key: K, value: ProjectDraft[K]) =>
    setForm((previous) => ({ ...previous, [key]: value }))

  const setTarget = (index: number, target: ProjectTarget) =>
    set(
      'targets',
      form.targets.map((entry, position) => (position === index ? target : entry)),
    )

  return (
    <form
      className="flex flex-col gap-3"
      onSubmit={(event) => {
        event.preventDefault()
        save.mutate({ id, draft: form }, { onSuccess: onDone })
      }}
    >
      <div className="flex flex-wrap items-end gap-3">
        <label className="flex flex-col gap-1 text-sm">
          <span className="text-zinc-700">Dépôt</span>
          <input
            value={form.repository}
            onChange={(event) => set('repository', event.target.value)}
            placeholder="CNOUS/mse"
            className="w-56 rounded-md border border-zinc-300 bg-white px-2 py-1 font-mono text-sm text-zinc-900"
          />
        </label>

        <label className="flex flex-col gap-1 text-sm">
          <span className="text-zinc-700">Libellé</span>
          <input
            value={form.label}
            onChange={(event) => set('label', event.target.value)}
            placeholder="le nom du dépôt à défaut"
            className="w-48 rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm text-zinc-900"
          />
        </label>

        <label className="flex flex-col gap-1 text-sm">
          <span className="text-zinc-700">Forge</span>
          <input
            value={form.forge_url}
            onChange={(event) => set('forge_url', event.target.value)}
            placeholder="https://forge.cadoles.com"
            className="w-64 rounded-md border border-zinc-300 bg-white px-2 py-1 font-mono text-sm text-zinc-900"
          />
        </label>
      </div>

      <div className="flex flex-wrap items-end gap-3">
        <label className="flex flex-col gap-1 text-sm">
          <span className="text-zinc-700">Dockerfile</span>
          <input
            value={form.dockerfile}
            onChange={(event) => set('dockerfile', event.target.value)}
            placeholder="vide : dépôt cloné sans être construit"
            className="w-80 rounded-md border border-zinc-300 bg-white px-2 py-1 font-mono text-sm text-zinc-900"
          />
        </label>

        <label className="flex flex-col gap-1 text-sm">
          <span className="text-zinc-700">Contexte</span>
          <input
            value={form.build_context}
            onChange={(event) => set('build_context', event.target.value)}
            className="w-24 rounded-md border border-zinc-300 bg-white px-2 py-1 font-mono text-sm text-zinc-900"
          />
        </label>
      </div>

      {form.dockerfile && (
        <>
          <label className="flex items-center gap-2 text-sm text-zinc-700">
            <input
              type="checkbox"
              checked={form.multi_stage}
              onChange={(event) => set('multi_stage', event.target.checked)}
              className="size-3.5"
            />
            Dockerfile à plusieurs cibles
          </label>

          {form.multi_stage && (
            <div className="flex flex-col gap-2">
              {/* Deux champs et non un : la cible `cli` publie `portal-cli`, et
                  renommer l'une ne doit pas obliger à renommer l'autre. */}
              <span className="text-xs text-zinc-500">
                Chaque étape constructible, et le dépôt d'image qu'elle alimente.
              </span>

              {form.targets.map((target, index) => (
                <div key={index} className="flex items-center gap-2">
                  <input
                    value={target.stage}
                    onChange={(event) => setTarget(index, { ...target, stage: event.target.value })}
                    placeholder="cible"
                    className="w-32 rounded-md border border-zinc-300 bg-white px-2 py-1 font-mono text-sm text-zinc-900"
                  />
                  <Icon name="arrow-right" className="size-3.5 text-zinc-400" />
                  <input
                    value={target.image}
                    onChange={(event) => setTarget(index, { ...target, image: event.target.value })}
                    placeholder="image"
                    className="w-48 rounded-md border border-zinc-300 bg-white px-2 py-1 font-mono text-sm text-zinc-900"
                  />
                  <button
                    type="button"
                    onClick={() =>
                      set(
                        'targets',
                        form.targets.filter((_, position) => position !== index),
                      )
                    }
                    aria-label={`Retirer l'étape ${target.stage || index + 1}`}
                    className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
                  >
                    <Icon name="cross" className="size-3.5" />
                  </button>
                </div>
              ))}

              <button
                type="button"
                onClick={() => set('targets', [...form.targets, { stage: '', image: '' }])}
                className="w-fit rounded-md border border-zinc-300 bg-white px-2 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
              >
                Ajouter une étape
              </button>
            </div>
          )}
        </>
      )}

      {/* Changer de dépôt efface le constat d'accès : il portait sur ce qu'on ne
          pilote plus, et le garder afficherait « accès vérifié » sur un dépôt que
          personne n'a testé. Le dire avant plutôt que de laisser découvrir une
          pastille disparue. */}
      {id !== undefined && form.repository !== draft.repository && (
        <p className="text-xs text-warn-700">
          Le dépôt change : le constat d'accès sera effacé, et le test à refaire.
        </p>
      )}

      {save.isError && (
        <p className="text-sm text-critical-700">{(save.error as Error).message}</p>
      )}

      <div className="flex items-center gap-2">
        <button
          type="submit"
          disabled={!form.repository.trim() || save.isPending}
          className="rounded-md bg-zinc-900 px-3 py-1.5 text-sm font-medium text-white transition-colors hover:bg-zinc-700 disabled:opacity-40"
        >
          {id === undefined ? 'Déclarer' : 'Enregistrer'}
        </button>
        <button
          type="button"
          onClick={onDone}
          className="rounded-md border border-zinc-300 bg-white px-3 py-1.5 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
        >
          Annuler
        </button>
      </div>
    </form>
  )
}
