import { useState } from 'react'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Icon } from '../../components/Icon'
import { copyText } from '../../lib/clipboard'
import { formatDateTime } from '../../lib/format'
import { useRotateDeployKey, useSaveProject, type Project, type ProjectDraft } from './api'

/*
 * Le formulaire d'un dépôt piloté, et ce qui l'accompagne.
 *
 * Extrait de l'écran de liste quand chaque projet a reçu sa page : le formulaire
 * y est le contenu principal, alors qu'il n'était qu'une ligne dépliée. Le garder
 * dans la liste aurait fait vivre la même saisie à deux endroits.
 */

export const EMPTY: ProjectDraft = {
  slug: '',
  repository: '',
  label: '',
  forge_url: '',
}

export function toDraft(project: Project): ProjectDraft {
  return {
    repository: project.repository,
    label: project.label,
    forge_url: project.forge_url,
    slug: project.slug,
  }
}

/**
 * L'état d'accès constaté.
 *
 * Trois états et non deux : jamais testé n'est pas la même chose qu'un test qui a
 * échoué, et les confondre ferait chercher un défaut de droits là où personne n'a
 * encore regardé.
 */
export function AccessBadge({ project }: { project: Project }) {
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

export function ProjectForm({
  id,
  draft,
  onDone,
  onSaved,
}: {
  id?: number
  draft: ProjectDraft
  /** Absent sur la page d'un projet : il n'y a pas de ligne dépliée à replier, le
   *  formulaire **est** l'écran. */
  onDone?: () => void
  /** Après l'enregistrement, avec le nom du projet dans les URL. Absent : `onDone`. */
  onSaved?: (saved: { id: number; slug?: string }) => void
}) {
  const [form, setForm] = useState<ProjectDraft>(draft)
  const save = useSaveProject()

  const set = <K extends keyof ProjectDraft>(key: K, value: ProjectDraft[K]) =>
    setForm((previous) => ({ ...previous, [key]: value }))

  return (
    <form
      className="flex flex-col gap-3"
      onSubmit={(event) => {
        event.preventDefault()
        save.mutate({ id, draft: form }, { onSuccess: (saved) => (onSaved ? onSaved(saved) : onDone?.()) })
      }}
    >
      <div className="flex flex-wrap items-end gap-3">
        <label className="flex flex-col gap-1 text-sm">
          <span className="text-zinc-700">Dépôt</span>
          <input
            value={form.repository}
            onChange={(event) => set('repository', event.target.value)}
            placeholder="team/mstore"
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

      {/* Le nom du projet dans les URL. Laissé vide à la création, l'API le dérive
          du dépôt — `vcarroy/mstore` donne `mstore`. Modifiable, parce que ces adresses
          sont mises en favori et collées dans des tickets. */}
      <label className="flex flex-col gap-1 text-sm">
        <span className="text-zinc-700">Nom dans les URL</span>
        <div className="flex items-center gap-2">
          <span className="font-mono text-xs text-zinc-500">/projects/</span>
          <input
            value={form.slug ?? ''}
            onChange={(event) => set('slug', event.target.value)}
            placeholder="dérivé du dépôt"
            className="w-56 rounded-md border border-zinc-300 bg-white px-2 py-1 font-mono text-sm text-zinc-900"
          />
        </div>
      </label>

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

/**
 * La clé de déploiement propre à un projet.
 *
 * Moitié **publique** : l'afficher n'expose rien, c'est précisément ce qu'on
 * colle dans les réglages du dépôt. La moitié privée n'a jamais quitté le
 * cluster et n'est disponible nulle part — pas même pour l'administrateur.
 *
 * Tant qu'aucune clé n'a été posée, l'écran dit qu'elle se prépare plutôt que
 * d'afficher un vide qu'on prendrait pour une panne.
 */

/**
 * La clé de déploiement d'un projet, et l'état constaté de sa pose.
 *
 * Le sort de la clé n'est plus **déclaré** mais **constaté** : le test d'accès
 * lit les clés de déploiement du dépôt et y cherche celle du projet. Il y avait
 * là un bouton « confirmez qu'elle y est » — une intention, avec le défaut des
 * intentions : elle restait vraie après qu'on avait retiré la clé du dépôt, et
 * les pipelines échouaient alors sur une erreur SSH que rien ne rattachait à
 * l'écran qui affirmait le contraire.
 */
export function ProjectKey({ project }: { project: Project }) {
  const rotate = useRotateDeployKey()
  const [state, setState] = useState<'idle' | 'copied' | 'failed'>('idle')
  const [confirming, setConfirming] = useState(false)

  if (!project.deploy_key) {
    // Le refus du worker plutôt qu'une promesse : sans lui, l'écran annonçait une clé « au
    // prochain passage » qui ne venait jamais.
    if (project.deploy_key_error) {
      return (
        <p className="flex items-start gap-1.5 text-xs text-critical-700">
          <Icon name="alert" className="mt-0.5 size-3.5 shrink-0" />
          <span>
            La clé n'a pas pu être posée : {project.deploy_key_error}. Le worker réessaie à chaque
            minute ; les constructions de ce projet refusent de partir d'ici là.
          </span>
        </p>
      )
    }
    return (
      <p className="text-xs text-zinc-500">
        Clé en préparation par le worker, en quelques secondes. Les constructions de ce projet
        partent une fois la clé en place dans le dépôt.
      </p>
    )
  }

  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-start gap-2">
        <code className="min-w-0 flex-1 break-all rounded-md border border-zinc-200 bg-zinc-100 px-2 py-1 font-mono text-[11px] text-zinc-900">
          {project.deploy_key}
        </code>
        <button
          type="button"
          onClick={() => {
            void copyText(project.deploy_key as string).then((done) =>
              setState(done ? 'copied' : 'failed'),
            )
          }}
          className="flex shrink-0 items-center gap-1 rounded-md border border-zinc-300 bg-white px-2 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
        >
          <Icon name={state === 'copied' ? 'check' : 'link'} className="size-3.5" />
          {state === 'copied' ? 'Copiée' : 'Copier'}
        </button>
        <button
          type="button"
          onClick={() => setConfirming(true)}
          disabled={rotate.isPending}
          className="shrink-0 rounded-md border border-zinc-300 bg-white px-2 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-40"
        >
          Remplacer
        </button>
      </div>

      {/* Constaté, jamais affirmé : la vérification lit les clés du dépôt et
          tranche. Le dashboard ne peut toujours pas coller la clé —
          cela appartient à un humain — mais il n'a plus besoin qu'on le lui
          confirme. */}
      {project.deploy_key_active ? (
        <p className="flex items-center gap-1.5 text-xs text-ok-700">
          <Icon name="check" className="size-3.5" />
          En place dans le dépôt : les pipelines de ce projet l'emploient.
        </p>
      ) : (
        <p className="text-xs text-zinc-500">
          Pas encore constatée dans le dépôt : collez-la dans ses clés de déploiement, la
          vérification est automatique. Tant qu'elle n'y est pas, les constructions de ce
          projet refusent de partir.
        </p>
      )}

      {state === 'failed' && (
        <p className="text-xs text-critical-700">
          Le navigateur a refusé la copie — sélectionnez le texte à la main.
        </p>
      )}

      {confirming && (
        <ConfirmDialog
          title="Remplacer la clé de ce dépôt ?"
          confirmLabel="Remplacer"
          pending={rotate.isPending}
          onCancel={() => setConfirming(false)}
          onConfirm={() => {
            rotate.mutate(project.id)
            setConfirming(false)
          }}
        >
          <p>
            Une nouvelle paire sera engendrée pour{' '}
            <span className="font-mono text-zinc-900">{project.repository}</span>.
          </p>
          <p className="text-zinc-500">
            L'ancienne clé cessera d'ouvrir le dépôt dès que la nouvelle sera posée : il faudra la
            remplacer dans les réglages du dépôt, sans quoi les pipelines échoueront au clone.
          </p>
        </ConfirmDialog>
      )}
    </div>
  )
}

