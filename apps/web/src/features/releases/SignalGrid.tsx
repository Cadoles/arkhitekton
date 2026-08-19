import { Link } from 'react-router'
import { Icon, type IconName } from '../../components/Icon'
import { status } from '../../lib/status'
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

export function SignalGrid({ tag, signals }: { tag: string; signals: SignalScore[] }) {
  return (
    <ul className="grid grid-cols-2 gap-3 lg:grid-cols-3">
      {signals.map((signal) => (
        <li key={signal.signal}>
          <SignalTile tag={tag} signal={signal} />
        </li>
      ))}
    </ul>
  )
}

function SignalTile({ tag, signal }: { tag: string; signal: SignalScore }) {
  const style = signal.health === 'na' ? undefined : status.health(signal.health)
  const href = signalHref(tag, signal.signal)

  const body = (
    <>
      <div className="flex items-center gap-2 text-zinc-500">
        <Icon name={SIGNAL_ICON[signal.signal] ?? 'doc'} className="size-4 shrink-0" />
        <span className="truncate text-sm font-medium">
          {SIGNAL_LABEL[signal.signal] ?? signal.signal}
        </span>
        {style && <Icon name={style.icon} className={`ml-auto size-4 shrink-0 ${style.text}`} />}
      </div>

      <p className="font-mono text-lg font-semibold text-zinc-900">
        {/* Un signal non mesuré n'est pas un signal à zéro : les deux
            afficheraient le même chiffre, et les confondre ferait lire comme un
            échec ce qui n'a simplement pas tourné (B11). */}
        {signal.measured ? Math.round(signal.value) : '—'}
      </p>

      {signal.detail && <p className="text-xs text-zinc-500">{signal.detail}</p>}

      {signal.measured && (
        // Le poids **effectif**, après redistribution : c'est lui qui explique la
        // contribution réelle au score, et le poids déclaré induirait en erreur
        // dès qu'un signal manque.
        <p className="mt-auto font-mono text-xs text-zinc-400">
          poids {Math.round(signal.weight)} %
        </p>
      )}
    </>
  )

  const shell = `flex h-full flex-col gap-1.5 rounded-lg border bg-white p-3 text-left ${
    style?.border ?? 'border-zinc-200'
  }`

  if (!href) return <div className={shell}>{body}</div>

  return (
    <Link
      to={href}
      className={`${shell} transition-shadow hover:shadow-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-zinc-400 focus-visible:ring-offset-1`}
    >
      {body}
    </Link>
  )
}
