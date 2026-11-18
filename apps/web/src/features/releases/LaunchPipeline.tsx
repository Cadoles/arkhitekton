import { useMemo, useState } from 'react'
import { Icon } from '../../components/Icon'
import { Modal } from '../../components/Modal'
import { Select } from '../../components/Select'
import { hasAtLeast, useSession } from '../auth/session'
import { useQueue, type Kind } from '../queue/api'
import { useLaunchOn } from './api'
import type { ResultSubject } from './tests'

/*
 * Lancer un pipeline sur une release ou une pull request.
 *
 * Ce que le sujet détermine n'est pas demandé : le serveur l'impose — la
 * révision construite, l'étiquette, les images construites dessous, et pour une
 * pull request son numéro. Le reste se saisit, avec les suggestions de la file.
 */

/** Ce que le sujet remplit. Une image se suggère sous `image:<dépôt>`. */
function filledBySubject(subject: ResultSubject, suggest: string | undefined): boolean {
  return (
    (subject.kind === 'pull' && suggest === 'pull-request') ||
    suggest === 'git-ref' ||
    suggest === 'release-tag' ||
    suggest === 'image' ||
    Boolean(suggest?.startsWith('image:'))
  )
}

export function LaunchPipelineButton({
  subject,
  disabled,
  disabledReason,
}: {
  subject: ResultSubject
  disabled?: boolean
  disabledReason?: string
}) {
  const session = useSession()
  const [open, setOpen] = useState(false)
  const canLaunch = session.kind === 'authenticated' && hasAtLeast(session.identity.role, 'operator')
  if (!canLaunch) return null

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        disabled={disabled}
        title={disabled ? disabledReason : undefined}
        className="flex items-center gap-1.5 rounded-md border border-zinc-900 bg-zinc-900 px-3 py-1.5 text-sm font-medium text-white transition-colors hover:bg-zinc-700 disabled:cursor-not-allowed disabled:opacity-40"
      >
        <Icon name="beaker" className="size-4" />
        Lancer un pipeline
      </button>
      {open && <LaunchModal subject={subject} onClose={() => setOpen(false)} />}
    </>
  )
}

function LaunchModal({ subject, onClose }: { subject: ResultSubject; onClose: () => void }) {
  const { data } = useQueue()
  const launch = useLaunchOn(subject)
  const kinds = useMemo(() => data?.kinds ?? [], [data])
  const [selected, setSelected] = useState('')
  const [values, setValues] = useState<Record<string, string>>({})

  const kind: Kind | undefined = kinds.find((entry) => entry.kind === selected) ?? kinds[0]
  const asked = (kind?.inputs ?? []).filter((input) => !filledBySubject(subject, input.suggest))

  const submit = () => {
    if (!kind) return
    const params: Record<string, string> = {}
    for (const input of asked) {
      const value = values[input.name]?.trim()
      if (value) params[input.name] = value
    }
    launch.mutate({ kind: kind.kind, params }, { onSuccess: onClose })
  }

  return (
    <Modal title={`Lancer un pipeline sur ${subject.label}`} onClose={onClose}>
      <form
        className="flex flex-col gap-3"
        onSubmit={(event) => {
          event.preventDefault()
          submit()
        }}
      >
        <label className="flex flex-col gap-1 text-sm">
          <span className="font-medium text-zinc-700">Pipeline</span>
          <Select
            value={kind?.kind ?? ''}
            onChange={(event) => {
              setSelected(event.target.value)
              setValues({})
            }}
            size="md"
          >
            {kinds.map((entry) => (
              <option key={entry.kind} value={entry.kind}>
                {entry.label}
              </option>
            ))}
          </Select>
        </label>

        <p className="text-xs text-zinc-500">
          {subject.kind === 'pull'
            ? 'Le numéro de la pull request, la révision de sa tête, son étiquette et les images construites dessous sont remplis par la pull request.'
            : 'La révision construite, l’étiquette de la release et les images construites dessous sont remplies par la release.'}
        </p>

        {asked.map((input) => (
          <label key={input.name} className="flex flex-col gap-1 text-sm">
            <span className="font-medium text-zinc-700">
              {input.name}
              {input.required && <span className="text-critical-700"> *</span>}
            </span>
            <input
              value={values[input.name] ?? ''}
              onChange={(event) => setValues({ ...values, [input.name]: event.target.value })}
              list={input.suggest ? `suggest-${input.name}` : undefined}
              className="rounded-md border border-zinc-300 bg-white px-2 py-1.5 font-mono text-zinc-900"
            />
            {input.suggest && (
              <datalist id={`suggest-${input.name}`}>
                {(data?.suggestions[input.suggest] ?? []).map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </datalist>
            )}
          </label>
        ))}

        {launch.isError && (
          <p className="flex items-start gap-1 text-sm text-critical-700">
            <Icon name="alert" className="mt-0.5 size-4 shrink-0" />
            {(launch.error as Error).message}
          </p>
        )}

        <div className="flex justify-end gap-2">
          <button type="button" onClick={onClose} className="rounded-md border border-zinc-300 px-3 py-1.5 text-sm">
            Annuler
          </button>
          <button
            type="submit"
            disabled={!kind || launch.isPending}
            className="rounded-md border border-zinc-900 bg-zinc-900 px-3 py-1.5 text-sm font-medium text-white disabled:opacity-40"
          >
            {launch.isPending ? 'Lancement…' : 'Lancer'}
          </button>
        </div>
      </form>
    </Modal>
  )
}
