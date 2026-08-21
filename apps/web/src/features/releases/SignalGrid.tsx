import { useState } from 'react'
import { Icon, type IconName } from '../../components/Icon'
import { Modal } from '../../components/Modal'
import { status } from '../../lib/status'
import { hasAtLeast, useSession } from '../auth/session'
import { useVerify, type ReleaseJob } from './api'
import { BenchDetail } from './BenchDetail'
import { FamilyDetail } from './FamilyDetail'
import { SecurityDetail } from './SecurityDetail'
import { familyForSignal, KIND_LABEL, kindForSignal, runningJobFor } from './signals'
import { SIGNAL_LABEL, type SignalScore } from './tests'

/*
 * Les signaux du score, en tuiles — repris de la maquette.
 *
 * La maquette en montrait quatre : qualité du code, tests, bench, sécurité. Le
 * modèle porté ici en compte six, les trois familles de tests y étant distinctes.
 * C'est plus fin, et le regrouper pour tenir en quatre tuiles perdrait ce que
 * l'API rend : une release dont les tests unitaires passent et dont les E2E
 * échouent n'est pas dans le même état qu'une release à moitié testée.
 *
 * La grille passe donc à trois colonnes au lieu de quatre, et la grammaire de la
 * tuile — bordure colorée par le verdict, icône du signal à gauche, icône du
 * verdict à droite, valeur en chiffres — reste celle de la maquette.
 */

const SIGNAL_ICON: Record<string, IconName> = {
  unit: 'beaker',
  integration: 'beaker',
  e2e: 'beaker',
  code: 'check',
  security: 'shield',
  performance: 'gauge',
}

export function SignalGrid({
  tag,
  signals,
  jobs,
}: {
  tag: string
  signals: SignalScore[]
  jobs: ReleaseJob[]
}) {
  return (
    <ul className="grid grid-cols-2 gap-3 lg:grid-cols-3">
      {signals.map((signal) => (
        <li key={signal.signal}>
          <SignalTile tag={tag} signal={signal} jobs={jobs} />
        </li>
      ))}
    </ul>
  )
}

function SignalTile({
  tag,
  signal,
  jobs,
}: {
  tag: string
  signal: SignalScore
  jobs: ReleaseJob[]
}) {
  const session = useSession()
  const verify = useVerify(tag)
  const [open, setOpen] = useState(false)

  const runningJob = runningJobFor(signal.signal, jobs)
  const running = runningJob !== undefined
  const kind = kindForSignal(signal.signal)
  // Chaque signal a son détail, et tous s'ouvrent en place : une famille de
  // tests montre ses cas, la sécurité ses constats, la performance ses
  // campagnes. Aucun ne mène plus à une page — perdre le score et les cinq
  // autres signaux pour lire une liste coûtait plus que ça ne rapportait.
  const family = familyForSignal(signal.signal)
  const hasDetail = family !== undefined || signal.signal === 'security' || signal.signal === 'performance'

  const canReplay =
    session.kind === 'authenticated' && hasAtLeast(session.identity.role, 'operator')

  // Pendant une exécution, la bordure de verdict est mise de côté : elle décrit
  // la mesure précédente, et la garder colorée ferait lire comme actuel un
  // résultat qu'on est en train de remplacer.
  const style = running || signal.health === 'na' ? undefined : status.health(signal.health)

  const shell = `flex h-full w-full flex-col gap-1.5 rounded-lg border bg-white p-3 text-left ${
    running ? 'animate-pulse border-zinc-400' : (style?.border ?? 'border-zinc-200')
  }`

  const body = (
    <>
      <div className="flex items-center gap-2 text-zinc-500">
        <Icon name={SIGNAL_ICON[signal.signal] ?? 'doc'} className="size-4 shrink-0" />
        <span className="truncate text-sm font-medium">
          {SIGNAL_LABEL[signal.signal] ?? signal.signal}
        </span>

        {running ? (
          <Spinner className="ml-auto size-4 shrink-0 text-zinc-400" />
        ) : (
          style && <Icon name={style.icon} className={`ml-auto size-4 shrink-0 ${style.text}`} />
        )}
      </div>

      <p className="font-mono text-lg font-semibold text-zinc-900">
        {/* Un signal non mesuré n'est pas un signal à zéro : les deux
            afficheraient le même chiffre, et les confondre ferait lire comme un
            échec ce qui n'a simplement pas tourné (B11). */}
        {running ? '…' : signal.measured ? Math.round(signal.value) : '—'}
      </p>

      <p className="text-xs text-zinc-500">
        {running ? 'mesure en cours…' : signal.detail}
      </p>

      <div className="mt-auto flex items-center gap-2 pt-1">
        {signal.measured && !running && (
          // Le poids **effectif**, après redistribution : c'est lui qui explique
          // la contribution réelle au score, et le poids déclaré induirait en
          // erreur dès qu'un signal manque.
          <span className="font-mono text-xs text-zinc-400">
            poids {Math.round(signal.weight)} %
          </span>
        )}

        {kind && canReplay && (
          <button
            type="button"
            onClick={() => verify.mutate(kind)}
            disabled={running || verify.isPending}
            title={`Relancer ${KIND_LABEL[kind] ?? kind}`}
            aria-label={`Relancer ${KIND_LABEL[kind] ?? kind}`}
            className="ml-auto rounded-md border border-zinc-300 bg-white px-1.5 py-0.5 text-xs font-medium text-zinc-600 transition-colors hover:bg-zinc-100 disabled:cursor-not-allowed disabled:opacity-40"
          >
            {verify.isPending ? 'Relance…' : 'Rejouer'}
          </button>
        )}
      </div>

      {verify.isError && (
        <p className="flex items-start gap-1 text-xs text-critical-700">
          <Icon name="alert" className="mt-0.5 size-3 shrink-0" />
          {(verify.error as Error).message}
        </p>
      )}
    </>
  )

  // Un signal sans détail reste une tuile inerte : un bouton qui n'ouvre rien se
  // clique une fois et déçoit.
  if (!hasDetail) return <div className={shell}>{body}</div>

  return (
    <>
      {/* La tuile entière est cliquable, sauf le bouton de relance qu'elle
          contient. Un `<button>` englobant l'aurait avalé — on ne peut pas
          imbriquer deux boutons. */}
      <div className={`relative ${shell}`}>
        <button
          type="button"
          onClick={() => setOpen(true)}
          aria-label={`Ouvrir le détail : ${SIGNAL_LABEL[signal.signal] ?? signal.signal}`}
          className="absolute inset-0 z-0 rounded-lg focus:outline-none focus-visible:ring-2 focus-visible:ring-zinc-400 focus-visible:ring-offset-1"
        />
        {/* Le contenu passe au-dessus de la surface cliquable, sans quoi le
            bouton de relance ne recevrait pas ses propres clics. */}
        <div className="pointer-events-none relative z-10 flex h-full flex-col gap-1.5 [&_button]:pointer-events-auto">
          {body}
        </div>
      </div>

      {open && (
        <Modal
          title={`${SIGNAL_LABEL[signal.signal] ?? signal.signal} — ${tag}`}
          onClose={() => setOpen(false)}
          wide
        >
          {family && (
            <FamilyDetail tag={tag} family={family} runName={runningJob?.run_name} />
          )}
          {signal.signal === 'security' && (
            <SecurityDetail tag={tag} runName={runningJob?.run_name} />
          )}
          {/* La performance n'a pas de pipeline que le dashboard lance : ses
              campagnes viennent d'un runner extérieur (B16), d'où l'absence de
              frise. */}
          {signal.signal === 'performance' && <BenchDetail tag={tag} />}
        </Modal>
      )}
    </>
  )
}

/**
 * Anneau tournant.
 *
 * Un `animate-spin` sur un cercle partiel plutôt qu'une icône du jeu : celui-ci
 * est statique par construction, et la seule chose qui doive bouger ici est
 * l'attente.
 */
export function Spinner({ className = '' }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={`animate-spin ${className}`} aria-hidden="true">
      <circle
        cx="12"
        cy="12"
        r="9"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        className="opacity-25"
      />
      <path
        d="M21 12a9 9 0 0 0-9-9"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
      />
    </svg>
  )
}
