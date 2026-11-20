import { useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Card, EmptyState, ErrorState, Skeleton } from '../../components/primitives'
import { useDeleteProject, useProjects, type Project } from './api'
import { AccessBadge, EMPTY, ProjectForm } from './ProjectForm'
import { inService } from './SetupCard'

/*
 * La liste des dépôts que le dashboard pilote.
 *
 * Elle ne fait plus que **lister**. Le réglage d'un projet a sa propre page —
 * `/admin/projects/:slug` — parce qu'il ne tient plus dans une ligne dépliée : à
 * ses paramètres généraux se sont ajoutés ses plugins, ses pipelines et l'état de
 * son suivi, qui sont quatre sujets distincts et non quatre sections d'un
 * formulaire.
 *
 * La déclaration d'un dépôt reste ici : elle appartient à la liste, pas à un
 * projet qui n'existe pas encore.
 */

export function ProjectsScreen() {
  const { data, isPending, error, refetch } = useProjects()
  const [adding, setAdding] = useState(false)
  const navigate = useNavigate()

  return (
    <div className="flex flex-col gap-4">
      {isPending && <Skeleton className="h-64 w-full" />}
      {error && (
        <ErrorState message="Les projets n'ont pas pu être chargés." onRetry={() => void refetch()} />
      )}

      {data && (
        <Card
          title={`Dépôts pilotés (${data.projects.length})`}
          action={
            !adding && (
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
                draft={{
                  ...EMPTY,
                  forge_url: data.forge_url,
                  image_registry: data.image_registry,
                }}
                onDone={() => setAdding(false)}
                // Le projet déclaré se met en service depuis sa page : y aller tout de suite
                // plutôt que de le laisser dans la liste à attendre un test.
                onSaved={(saved) => {
                  setAdding(false)
                  if (saved.slug) navigate(`/admin/projects/${encodeURIComponent(saved.slug)}`)
                }}
              />
            </div>
          )}

          {data.projects.length === 0 && !adding ? (
            <EmptyState>
              Aucun dépôt déclaré. Le dashboard emploie encore ceux de sa configuration.
            </EmptyState>
          ) : (
            <ul className="flex flex-col gap-2">
              {data.projects.map((project) => (
                <ProjectRow key={project.id} project={project} />
              ))}
            </ul>
          )}
        </Card>
      )}
    </div>
  )
}

function ProjectRow({ project }: { project: Project }) {
  const remove = useDeleteProject()
  const [confirming, setConfirming] = useState(false)

  return (
    <li className="flex flex-col gap-2 rounded-md border border-zinc-200 px-3 py-2">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        {/* Le libellé mène à la page du projet : c'est là que tout se règle
            désormais, et un lien depuis son nom est l'endroit où on le cherche. */}
        <Link
          to={`/admin/projects/${encodeURIComponent(project.slug)}`}
          className="font-medium text-zinc-900 hover:underline"
        >
          {project.label}
        </Link>

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
            {project.multi_stage ? `${project.targets.length} étape(s)` : 'image unique'}
          </span>
        ) : (
          // Un dépôt cloné sans être construit : c'est le cas de `mstore-deploy` et
          // de `mstore-pipelines`, que l'audit lit sans jamais en tirer d'image.
          <span className="rounded border border-zinc-200 px-1.5 py-0.5 text-xs text-zinc-500">
            sans construction
          </span>
        )}

        <AccessBadge project={project} />

        {/* Ce qui manque pour construire se règle depuis la page du projet, où la
            mise en service se suit seule. Constaté par la vérification, jamais
            affirmé à la main. */}
        {!inService(project) && (
          <Link
            to={`/admin/projects/${encodeURIComponent(project.slug)}`}
            className="rounded border border-warn-500 bg-warn-50 px-1.5 py-0.5 text-xs text-warn-700 hover:underline"
            title="Les constructions de ce projet refusent de partir tant que son dépôt ne se lit pas et que sa clé n'y est pas"
          >
            à mettre en service
          </Link>
        )}

        <div className="ml-auto flex items-center gap-2">
          <Link
            to={`/admin/projects/${encodeURIComponent(project.slug)}`}
            className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
          >
            Configurer
          </Link>
          <button
            type="button"
            onClick={() => setConfirming(true)}
            className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
          >
            Retirer
          </button>
        </div>
      </div>

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
            Rien n'est supprimé dans la forge. Ses réglages de plugins et de pipelines
            partent avec lui.
          </p>
        </ConfirmDialog>
      )}
    </li>
  )
}
