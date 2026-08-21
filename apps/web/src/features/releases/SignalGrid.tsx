import { Link } from 'react-router'
import { Icon, type IconName } from '../../components/Icon'
import { status } from '../../lib/status'
import { hasAtLeast, useSession } from '../auth/session'
import { useVerify, type ReleaseJob } from './api'
import { KIND_LABEL, kindForSignal, signalIsRunning } from './signals'
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

/**
 * Écran de détail d'un signal, quand il en a un.
 *
 * La maquette ouvrait une boîte de détail ; ici les écrans existent déjà et
 * portent bien plus que ce qu'une boîte afficherait — cas de test, journaux,
 * avis de sécurité. Un signal sans écran reste une tuile inerte plutôt que de
 * mener à une page vide.
 */
function signalHref(tag: string, signal: string): string | undefined {
  const encoded = encodeURIComponent(tag)
  switch (signal) {
    case 'unit':
    case 'integration':
    case 'e2e':
      return `/releases/${encoded}/tests/${signal}`
    case 'code':
      return `/releases/${encoded}/tests/static`
    case 'security':
      return `/releases/${encoded}/security`
    default:
      return undefined
  }
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

  const running = signalIsRunning(signal.signal, jobs)
  const kind = kindForSignal(signal.signal)
  const href = signalHref(tag, signal.signal)

  const canReplay =
    session.kind === 'authenticated' && hasAtLeast(session.identity.role, 'operator')

  // Pendant une exécution, la bordure de verdict est mise de côté : elle décrit
  // la mesure précédente, et la garder colorée ferait lire comme actuel un
  // résultat qu'on est en train de remplacer.
  const style = running || signal.health === 'na' ? undefined : status.health(signal.health)

  return (
    <div
      className={`flex h-full flex-col gap-1.5 rounded-lg border bg-white p-3 ${
        running ? 'animate-pulse border-zinc-400' : (style?.border ?? 'border-zinc-200')
      }`}
    >
      <div className="flex items-center gap-2 text-zinc-500">
        <Icon name={SIGNAL_ICON[signal.signal] ?? 'doc'} className="size-4 shrink-0" />
        {href ? (
          <Link
            to={href}
            className="truncate text-sm font-medium hover:text-zinc-900 hover:underline"
          >
            {SIGNAL_LABEL[signal.signal] ?? signal.signal}
          </Link>
        ) : (
          <span className="truncate text-sm font-medium">
            {SIGNAL_LABEL[signal.signal] ?? signal.signal}
          </span>
        )}

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
    </div>
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
