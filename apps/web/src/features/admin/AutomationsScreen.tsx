import { useState } from 'react'
import { useOutletContext } from 'react-router'
import { Card, EmptyState, Skeleton } from '../../components/primitives'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Icon } from '../../components/Icon'
import type { Project } from './api'
import {
  emptyPlan,
  gesteLabel,
  useAutomationCatalogue,
  useDeleteReaction,
  useReactions,
  useSaveReaction,
  type Catalogue,
  type Geste,
  type Plan,
  type Reaction,
  type ReactionDraft,
} from './automations'

/*
 * Les automatisations d'un projet.
 *
 * « Sur tel fait, exécuter tel plan. » L'écran ne code aucune liste : les faits
 * écoutables, les gestes disponibles et les conditions viennent du serveur, qui
 * les tire des registres du binaire.
 *
 * # Un éditeur structuré, pas un champ de texte
 *
 * Un YAML libre est le chemin le moins cher et le pire : il échoue à
 * l'enregistrement, sur un message de parseur, pour une virgule. Les étapes sont
 * des lignes, les gestes des vignettes qu'on ajoute dans une ligne.
 *
 * La forme du modèle rend cet éditeur possible — des étapes et des gestes
 * parallèles se dessinent en colonnes empilées. Un graphe libre aurait demandé un
 * éditeur de graphe, et c'est la seconde raison de ne pas en avoir fait un.
 */
export function AutomationsScreen() {
  const project = useOutletContext<Project>()
  const reactions = useReactions(project.slug)
  const catalogue = useAutomationCatalogue()
  const [editing, setEditing] = useState<Reaction | 'new' | null>(null)

  if (reactions.isPending || catalogue.isPending) {
    return <Skeleton className="h-64 w-full" />
  }

  return (
    <div className="flex flex-col gap-4">
      <Card
        title="Automatisations"
        action={
          <button
            type="button"
            onClick={() => setEditing('new')}
            className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
          >
            Déclarer une réaction
          </button>
        }
      >
        {(reactions.data?.reactions.length ?? 0) === 0 ? (
          <EmptyState>
            Aucune réaction déclarée. Une réaction dit ce qui s'enchaîne après un fait —
            poser un statut, lancer des vérifications, prévenir une messagerie.
          </EmptyState>
        ) : (
          <ul className="flex flex-col divide-y divide-zinc-200">
            {reactions.data?.reactions.map((entry) => (
              <ReactionRow
                key={entry.id}
                reaction={entry}
                catalogue={catalogue.data}
                slug={project.slug}
                onEdit={() => setEditing(entry)}
              />
            ))}
          </ul>
        )}
      </Card>

      {editing && catalogue.data && (
        <ReactionEditor
          slug={project.slug}
          catalogue={catalogue.data}
          reaction={editing === 'new' ? null : editing}
          onDone={() => setEditing(null)}
        />
      )}
    </div>
  )
}

function ReactionRow({
  reaction,
  catalogue,
  slug,
  onEdit,
}: {
  reaction: Reaction
  catalogue: Catalogue | undefined
  slug: string
  onEdit: () => void
}) {
  const remove = useDeleteReaction(slug)
  const [confirming, setConfirming] = useState(false)

  const event = catalogue?.events.find((choice) => choice.type === reaction.event_type)
  const gestes = reaction.plan.stages.flatMap((stage) => stage.gestes)

  return (
    <li className="flex flex-col gap-1 py-2">
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
        <span className="text-sm font-medium text-zinc-900">{reaction.name}</span>

        {!reaction.enabled && (
          <span className="rounded border border-zinc-300 bg-zinc-50 px-1.5 py-0.5 text-xs text-zinc-600">
            éteinte
          </span>
        )}

        {/* Inerte plutôt qu'effacée : une extinction peut être temporaire, et
            effacer ferait perdre une configuration. */}
        {reaction.inert && (
          <span
            className="rounded border border-critical-500 bg-critical-50 px-1.5 py-0.5 text-xs text-critical-700"
            title={reaction.inert_reason}
          >
            inerte
          </span>
        )}

        <span className="ml-auto flex gap-2">
          <button
            type="button"
            onClick={onEdit}
            className="rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
          >
            Modifier
          </button>
          <button
            type="button"
            onClick={() => setConfirming(true)}
            className="rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
          >
            Supprimer
          </button>
        </span>
      </div>

      <p className="text-xs text-zinc-500">
        sur <span className="font-mono">{event?.label ?? reaction.event_type}</span>
        {Object.keys(reaction.match).length > 0 && (
          <>
            {' '}
            si{' '}
            {Object.entries(reaction.match)
              .map(([key, value]) => `${key} = ${value}`)
              .join(' et ')}
          </>
        )}{' '}
        — {gestes.length === 0 ? 'aucun geste' : gestes.map((geste) => gesteLabel(catalogue, geste)).join(', ')}
      </p>

      {reaction.inert && reaction.inert_reason && (
        <p className="text-xs text-critical-700">{reaction.inert_reason}</p>
      )}

      {confirming && (
        <ConfirmDialog
          title={`Supprimer « ${reaction.name} » ?`}
          confirmLabel="Supprimer"
          pending={remove.isPending}
          onCancel={() => setConfirming(false)}
          onConfirm={() => {
            remove.mutate(reaction.id)
            setConfirming(false)
          }}
        >
          <p>Les exécutions déjà conclues restent consultables ; plus rien ne partira.</p>
        </ConfirmDialog>
      )}
    </li>
  )
}

/** L'éditeur d'une réaction. */
function ReactionEditor({
  slug,
  catalogue,
  reaction,
  onDone,
}: {
  slug: string
  catalogue: Catalogue
  reaction: Reaction | null
  onDone: () => void
}) {
  const save = useSaveReaction(slug)
  const [draft, setDraft] = useState<ReactionDraft>(() => ({
    name: reaction?.name ?? '',
    event_type: reaction?.event_type ?? catalogue.events[0]?.type ?? '',
    enabled: reaction?.enabled ?? true,
    match: reaction?.match ?? {},
    plan: reaction?.plan ?? emptyPlan(),
  }))

  const event = catalogue.events.find((choice) => choice.type === draft.event_type)
  const set = <K extends keyof ReactionDraft>(key: K, value: ReactionDraft[K]) =>
    setDraft((previous) => ({ ...previous, [key]: value }))

  return (
    <Card title={reaction ? `Modifier « ${reaction.name} »` : 'Déclarer une réaction'}>
      <form
        className="flex flex-col gap-4"
        onSubmit={(submit) => {
          submit.preventDefault()
          save.mutate({ id: reaction?.id, draft }, { onSuccess: onDone })
        }}
      >
        <div className="flex flex-wrap items-end gap-3">
          <label className="flex flex-col gap-1 text-sm">
            <span className="text-zinc-700">Nom</span>
            <input
              value={draft.name}
              onChange={(change) => set('name', change.target.value)}
              placeholder="Prévenir l'équipe"
              className="w-64 rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm text-zinc-900"
            />
          </label>

          <label className="flex flex-col gap-1 text-sm">
            <span className="text-zinc-700">Sur le fait</span>
            <select
              value={draft.event_type}
              onChange={(change) => set('event_type', change.target.value)}
              className="w-72 rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm text-zinc-900"
            >
              {catalogue.events.map((choice) => (
                <option key={choice.type} value={choice.type}>
                  {choice.label}
                  {choice.source ? ` (${choice.source})` : ''}
                </option>
              ))}
            </select>
          </label>

          <label className="flex items-center gap-2 text-sm text-zinc-700">
            <input
              type="checkbox"
              checked={draft.enabled}
              onChange={(change) => set('enabled', change.target.checked)}
            />
            Active
          </label>
        </div>

        <MatchEditor
          fields={event?.fields ?? []}
          match={draft.match}
          onChange={(match) => set('match', match)}
        />

        <PlanEditor
          catalogue={catalogue}
          fields={event?.fields ?? []}
          plan={draft.plan}
          onChange={(plan) => set('plan', plan)}
        />

        {save.error && (
          <p className="text-xs text-critical-700">{(save.error as Error).message}</p>
        )}

        <div className="flex gap-2">
          <button
            type="submit"
            disabled={save.isPending}
            className="rounded-md border border-zinc-300 bg-white px-3 py-1 text-sm font-medium text-zinc-800 transition-colors hover:bg-zinc-100 disabled:opacity-40"
          >
            Enregistrer
          </button>
          <button
            type="button"
            onClick={onDone}
            className="rounded-md px-3 py-1 text-sm text-zinc-600 hover:underline"
          >
            Annuler
          </button>
        </div>
      </form>
    </Card>
  )
}

/** L'appariement : égalité et appartenance, rien d'autre. */
function MatchEditor({
  fields,
  match,
  onChange,
}: {
  fields: string[]
  match: Record<string, string>
  onChange: (match: Record<string, string>) => void
}) {
  const entries = Object.entries(match)

  return (
    <fieldset className="flex flex-col gap-2">
      <legend className="text-sm text-zinc-700">Seulement si</legend>

      {entries.length === 0 && (
        <p className="text-xs text-zinc-500">
          Aucune condition : la réaction part sur tout fait de ce type.
        </p>
      )}

      {entries.map(([key, value]) => (
        <div key={key} className="flex flex-wrap items-center gap-2">
          <span className="font-mono text-xs text-zinc-700">{key}</span>
          <span className="text-xs text-zinc-500">vaut</span>
          <input
            value={value}
            onChange={(change) => onChange({ ...match, [key]: change.target.value })}
            placeholder="final"
            className="w-48 rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-sm text-zinc-900"
          />
          {/* L'appartenance s'écrit avec des virgules : elle se saisit dans un
              champ de texte et se relit sans documentation. */}
          <span className="text-xs text-zinc-400">plusieurs valeurs : séparées par des virgules</span>
          <button
            type="button"
            onClick={() => {
              const rest = { ...match }
              delete rest[key]
              onChange(rest)
            }}
            className="text-xs text-zinc-500 hover:underline"
          >
            retirer
          </button>
        </div>
      ))}

      {fields.length > 0 && (
        <select
          value=""
          onChange={(change) => {
            if (change.target.value) onChange({ ...match, [change.target.value]: '' })
          }}
          className="w-56 rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm text-zinc-900"
        >
          <option value="">ajouter une condition…</option>
          {fields
            .filter((field) => !(field in match))
            .map((field) => (
              <option key={field} value={field}>
                {field}
              </option>
            ))}
        </select>
      )}
    </fieldset>
  )
}

/** Le plan : des étapes, des gestes parallèles. */
function PlanEditor({
  catalogue,
  fields,
  plan,
  onChange,
}: {
  catalogue: Catalogue
  fields: string[]
  plan: Plan
  onChange: (plan: Plan) => void
}) {
  const setStage = (index: number, stage: Plan['stages'][number]) =>
    onChange({ stages: plan.stages.map((entry, rank) => (rank === index ? stage : entry)) })

  return (
    <fieldset className="flex flex-col gap-3">
      <legend className="text-sm text-zinc-700">Alors</legend>

      {plan.stages.map((stage, index) => (
        <div key={index} className="rounded-md border border-zinc-200 p-3">
          <div className="mb-2 flex items-baseline gap-2">
            <span className="text-xs font-medium text-zinc-700">Étape {index + 1}</span>
            <span className="text-xs text-zinc-400">
              {stage.gestes.length > 1 ? 'gestes exécutés en parallèle' : ''}
            </span>
            {plan.stages.length > 1 && (
              <button
                type="button"
                onClick={() =>
                  onChange({ stages: plan.stages.filter((_, rank) => rank !== index) })
                }
                className="ml-auto text-xs text-zinc-500 hover:underline"
              >
                retirer l'étape
              </button>
            )}
          </div>

          <div className="flex flex-col gap-2">
            {stage.gestes.map((geste, rank) => (
              <GesteRow
                key={rank}
                catalogue={catalogue}
                fields={fields}
                geste={geste}
                first={index === 0}
                onChange={(next) =>
                  setStage(index, {
                    ...stage,
                    gestes: stage.gestes.map((entry, position) =>
                      position === rank ? next : entry,
                    ),
                  })
                }
                onRemove={() =>
                  setStage(index, {
                    ...stage,
                    gestes: stage.gestes.filter((_, position) => position !== rank),
                  })
                }
              />
            ))}

            <select
              value=""
              onChange={(change) => {
                const choice = catalogue.gestes.find(
                  (entry) => `${entry.kind}:${entry.ref}` === change.target.value,
                )
                if (!choice) return
                setStage(index, {
                  ...stage,
                  gestes: [...stage.gestes, { kind: choice.kind, ref: choice.ref, params: {} }],
                })
              }}
              className="w-72 rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm text-zinc-900"
            >
              <option value="">ajouter un geste…</option>
              {catalogue.gestes.map((choice) => (
                <option key={`${choice.kind}:${choice.ref}`} value={`${choice.kind}:${choice.ref}`}>
                  {/* La source distingue deux gestes de même libellé : « Statut de
                      commit » ne parle pas à la même forge selon le plugin. */}
                  {choice.label} ({choice.source ?? (choice.kind === 'action' ? 'action' : 'pipeline')})
                </option>
              ))}
            </select>
          </div>
        </div>
      ))}

      <button
        type="button"
        onClick={() => onChange({ stages: [...plan.stages, { gestes: [] }] })}
        className="w-44 rounded-md border border-zinc-300 bg-white px-2 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
      >
        <Icon name="queue" className="mr-1 inline size-3.5" />
        Ajouter une étape
      </button>
    </fieldset>
  )
}

function GesteRow({
  catalogue,
  fields,
  geste,
  first,
  onChange,
  onRemove,
}: {
  catalogue: Catalogue
  fields: string[]
  geste: Geste
  /** Première étape : rien ne la précède, et une condition d'échec n'y partirait
   *  jamais — le serveur la refuse. */
  first: boolean
  onChange: (geste: Geste) => void
  onRemove: () => void
}) {
  const choice = catalogue.gestes.find(
    (entry) => entry.kind === geste.kind && entry.ref === geste.ref,
  )

  return (
    <div className="flex flex-col gap-1 rounded-md bg-zinc-50 p-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm text-zinc-900">{choice?.label ?? geste.ref}</span>
        <span className="rounded border border-zinc-300 px-1.5 py-0.5 text-xs text-zinc-600">
          {geste.kind === 'action' ? 'action' : 'pipeline'}
        </span>

        <select
          value={geste.when ?? 'success'}
          onChange={(change) => onChange({ ...geste, when: change.target.value as Geste['when'] })}
          className="rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-xs text-zinc-900"
        >
          <option value="success">si l'étape précédente a réussi</option>
          {/* Sur la première étape, `failure` ne partirait jamais : rien ne la
              précède. Le serveur le refuse, l'écran ne le propose pas. */}
          {!first && <option value="failure">si elle a échoué</option>}
          <option value="always">dans tous les cas</option>
        </select>

        <button
          type="button"
          onClick={onRemove}
          className="ml-auto text-xs text-zinc-500 hover:underline"
        >
          retirer
        </button>
      </div>

      {choice?.summary && <p className="text-xs text-zinc-500">{choice.summary}</p>}

      {choice?.fields?.map((field) => (
        <label key={field.name} className="flex flex-col gap-0.5 text-xs">
          <span className="text-zinc-700">
            {field.label}
            {field.required && <span className="text-critical-700"> *</span>}
          </span>
          <input
            value={geste.params?.[field.name] ?? ''}
            onChange={(change) =>
              onChange({
                ...geste,
                params: { ...geste.params, [field.name]: change.target.value },
              })
            }
            placeholder={field.placeholder}
            /* Une liste de suggestions, et non un sélecteur fermé : le serveur
               laisse un gabarit traverser un jeu fermé, parce qu'un état qui
               reflète l'issue du fait ne peut pas s'écrire en dur. Un `select`
               interdirait `{outcome}` que le moteur accepte. */
            list={field.options ? `${field.name}-options` : undefined}
            className="rounded-md border border-zinc-300 bg-white px-2 py-1 font-mono text-xs text-zinc-900"
          />
          {field.options && (
            <datalist id={`${field.name}-options`}>
              {field.options.map((option) => (
                <option key={option} value={option} />
              ))}
            </datalist>
          )}
          {field.help && <span className="text-zinc-500">{field.help}</span>}
        </label>
      ))}

      {/* Les variables disponibles viennent du fait écouté : les proposer évite
          d'interpoler une clé qui n'existe pas. */}
      {choice?.kind === 'action' && fields.length > 0 && (
        <p className="text-xs text-zinc-400">
          variables : {fields.map((field) => `{${field}}`).join(' ')}
        </p>
      )}
    </div>
  )
}
