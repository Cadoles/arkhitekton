import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Icon } from '../../components/Icon'
import { Card } from '../../components/primitives'
import { hasAtLeast, useSession } from '../auth/session'
import { useCreateRelease, type ReleaseKind } from '../releases/api'
import type { Version, VersionRelease } from './api'

/*
 * Création d'une release depuis le périmètre d'une version (doc 08 §8.4).
 *
 * C'est ici que le geste a du sens : on crée une release **pour** une version, au
 * vu de son avancement et de ses anomalies, affichés juste au-dessus. Le tag est
 * pré-rempli depuis le numéro de la version, et le nom Redmine est transmis pour
 * que le serveur vérifie la jointure (B17) — une release rattachée à la mauvaise
 * version produirait une note de release comparant des périmètres sans rapport.
 *
 * Aucune règle n'est pré-vérifiée côté client : cohérence tag/nature, permission
 * de livrer, existence de la source — tout est tranché par le serveur, et son
 * message d'erreur est affiché tel quel. Dupliquer ces règles ici les ferait
 * diverger (doc 07 §7.2).
 */

const KINDS: { value: ReleaseKind; label: string; hint: string }[] = [
  { value: 'pre', label: 'Pré-release', hint: 'construite puis testée, jusqu’en pré-production' },
  { value: 'final', label: 'Finale', hint: 're-tag bit à bit d’une pré-release validée' },
  { value: 'patch', label: 'Patch', hint: 'corrige une version déjà livrée' },
]

/**
 * Numéro suggéré depuis le nom de la version.
 *
 * Simple suggestion de saisie : le préfixe `v` ou `MSE ` est retiré pour proposer
 * `3.2.0-rc1` plutôt que `v3.2.0-rc1`. La vérification de jointure, elle, est
 * faite par le serveur avec la vraie normalisation — celle-ci n'en est pas une
 * copie, juste un confort qui peut se tromper sans conséquence.
 */
function suggestedBase(versionName: string): string {
  return versionName.trim().replace(/^(mse\s+|v(?=\d))/i, '')
}

// Le prochain numéro de RC libre, déduit des releases déjà produites : proposer
// `-rc1` alors que `-rc3` existe ferait échouer la création pour cause de doublon,
// avec une erreur que le formulaire aurait pu éviter.
function nextPrerelease(base: string, releases: VersionRelease[]): string {
  let highest = 0
  for (const release of releases) {
    const match = release.tag.match(new RegExp(`^${base.replaceAll('.', '\\.')}-rc(\\d+)$`))
    if (match) highest = Math.max(highest, Number(match[1]))
  }
  return `${base}-rc${highest + 1}`
}

export function CreateReleasePanel({
  version,
  releases,
}: {
  version: Version
  releases: VersionRelease[]
}) {
  const session = useSession()
  const create = useCreateRelease()

  const [open, setOpen] = useState(false)
  const [kind, setKind] = useState<ReleaseKind>('pre')
  const [tag, setTag] = useState('')
  const [sourceTag, setSourceTag] = useState('')
  const [testsGreen, setTestsGreen] = useState(false)
  const [confirming, setConfirming] = useState(false)

  const base = useMemo(() => suggestedBase(version.name), [version.name])
  const sources = releases.filter((release) => release.kind === 'pre')

  // Le rôle décide de ce qui est proposé, pas de ce qui est permis : masquer une
  // action interdite évite un clic qui échouera, la décision reste au serveur.
  const canCreate = session.kind === 'authenticated' && hasAtLeast(session.identity.role, 'operator')
  const canFinal = session.kind === 'authenticated' && hasAtLeast(session.identity.role, 'releaser')

  if (!canCreate) return null

  const selectKind = (next: ReleaseKind) => {
    setKind(next)
    // Le tag suit la nature tant qu'il n'a pas été édité à la main — le
    // recalculer après une saisie écraserait ce que l'utilisateur vient de taper.
    setTag(next === 'pre' ? nextPrerelease(base, releases) : base)
  }

  const openForm = () => {
    setOpen(true)
    setTag(nextPrerelease(base, releases))
  }

  const submit = () => {
    setConfirming(false)
    create.mutate({
      tag: tag.trim(),
      kind,
      // Le nom Redmine n'est transmis que s'il en est un : celui d'une provisoire
      // vient d'un tag, et le vérifier contre lui-même ne vérifierait rien.
      redmine_version: version.pending || kind === 'patch' ? undefined : version.name,
      source_tag: kind === 'pre' ? undefined : sourceTag,
      tests_green: kind === 'pre' ? undefined : testsGreen,
    })
  }

  return (
    <Card
      title="Créer une release"
      action={
        !open && (
          <button
            type="button"
            onClick={openForm}
            className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
          >
            Ouvrir
          </button>
        )
      }
    >
      {!open ? (
        <p className="text-sm text-zinc-500">
          Pré-release, finale ou patch pour <span className="font-mono">{version.name}</span>.
        </p>
      ) : (
        <form
          onSubmit={(event) => {
            event.preventDefault()
            setConfirming(true)
          }}
          className="flex flex-col gap-3"
        >
          <fieldset className="flex flex-wrap gap-2">
            <legend className="mb-1 text-sm text-zinc-500">Nature</legend>
            {KINDS.map((option) => {
              const locked = option.value !== 'pre' && !canFinal
              return (
                <label
                  key={option.value}
                  className={`flex items-start gap-2 rounded-md border px-3 py-2 text-sm ${
                    kind === option.value ? 'border-zinc-900 bg-zinc-50' : 'border-zinc-200'
                  } ${locked ? 'opacity-50' : 'cursor-pointer'}`}
                  title={locked ? 'Livrer exige le rôle releaser.' : undefined}
                >
                  <input
                    type="radio"
                    name="kind"
                    value={option.value}
                    checked={kind === option.value}
                    onChange={() => selectKind(option.value)}
                    disabled={locked}
                    className="mt-0.5"
                  />
                  <span>
                    <span className="block font-medium text-zinc-900">{option.label}</span>
                    <span className="block text-xs text-zinc-500">{option.hint}</span>
                  </span>
                </label>
              )
            })}
          </fieldset>

          <label className="flex flex-wrap items-center gap-2 text-sm">
            <span className="min-w-24 text-zinc-500">Tag</span>
            <input
              value={tag}
              onChange={(event) => setTag(event.target.value)}
              className="w-56 rounded-md border border-zinc-300 bg-white px-2 py-1 font-mono text-sm text-zinc-900"
            />
          </label>

          {kind !== 'pre' && (
            <>
              <label className="flex flex-wrap items-center gap-2 text-sm">
                <span className="min-w-24 text-zinc-500">Pré-release source</span>
                <select
                  value={sourceTag}
                  onChange={(event) => setSourceTag(event.target.value)}
                  className="rounded-md border border-zinc-300 bg-white px-2 py-1 font-mono text-sm text-zinc-900"
                >
                  <option value="">— choisir —</option>
                  {sources.map((source) => (
                    <option key={source.tag} value={source.tag}>
                      {source.tag} ({source.state})
                    </option>
                  ))}
                </select>
              </label>
              {sources.length === 0 && (
                <p className="flex items-start gap-2 text-sm text-warn-700">
                  <Icon name="alert" className="mt-0.5 size-4 shrink-0" />
                  Aucune pré-release pour cette version : une finale se produit par re-tag d'une RC
                  validée, jamais depuis rien (B17).
                </p>
              )}
              <label className="flex items-center gap-2 text-sm text-zinc-700">
                <input
                  type="checkbox"
                  checked={testsGreen}
                  onChange={(event) => setTestsGreen(event.target.checked)}
                />
                Je confirme que les résultats de la RC source sont au vert.
              </label>
            </>
          )}

          <div className="flex items-center gap-2">
            <button
              type="submit"
              disabled={create.isPending || tag.trim() === '' || (kind !== 'pre' && sourceTag === '')}
              className="rounded-md border border-zinc-900 bg-zinc-900 px-3 py-1 text-sm font-medium text-white transition-colors hover:bg-zinc-700 disabled:cursor-not-allowed disabled:opacity-40"
            >
              Créer
            </button>
            <button
              type="button"
              onClick={() => setOpen(false)}
              className="rounded-md border border-zinc-300 bg-white px-3 py-1 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
            >
              Annuler
            </button>
          </div>

          {create.isError && (
            <p className="flex items-start gap-2 text-sm text-critical-700">
              <Icon name="alert" className="mt-0.5 size-4 shrink-0" />
              {(create.error as Error).message}
            </p>
          )}
        </form>
      )}

      {create.isSuccess && (
        <div className="mt-3 border-t border-zinc-200 pt-3 text-sm">
          <p className="text-healthy-700">
            Release{' '}
            <Link
              to={`/releases/${encodeURIComponent(create.data.release.tag)}`}
              className="font-mono font-medium hover:underline"
            >
              {create.data.release.tag}
            </Link>{' '}
            créée.
          </p>
          {/* Les réserves du serveur : la création a réussi malgré elles, et les
              taire ferait croire à un succès sans condition. */}
          {(create.data.warnings ?? []).map((warning) => (
            <p key={warning} className="mt-1 flex items-start gap-2 text-warn-700">
              <Icon name="alert" className="mt-0.5 size-4 shrink-0" />
              {warning}
            </p>
          ))}
        </div>
      )}

      {confirming && (
        <ConfirmDialog
          title={`Créer la release ${tag.trim()}`}
          confirmLabel="Créer la release"
          // La ressaisie pour ce qui engage une livraison : une finale ou un patch
          // met un re-tag en file vers le registre — action sortante (doc 08 §8.5).
          // Une pré-release ne crée qu'un brouillon local : confirmation simple.
          challenge={kind === 'pre' ? undefined : tag.trim()}
          onConfirm={submit}
          onCancel={() => setConfirming(false)}
          pending={create.isPending}
        >
          <p>
            {KINDS.find((option) => option.value === kind)?.label} pour la version{' '}
            <span className="font-mono">{version.name}</span>
            {kind !== 'pre' && sourceTag && (
              <>
                {' '}
                — re-tag de <span className="font-mono">{sourceTag}</span>
              </>
            )}
            .
          </p>
        </ConfirmDialog>
      )}
    </Card>
  )
}
