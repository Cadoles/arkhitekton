import { useState } from 'react'
import { Icon, type IconName } from '../../components/Icon'
import { Modal } from '../../components/Modal'
import { status } from '../../lib/status'
import { hasAtLeast, useSession } from '../auth/session'
import { useVerify, type ReleaseJob } from './api'
import { BenchDetail } from './BenchDetail'
import { FamilyDetail } from './FamilyDetail'
import { SecurityDetail } from './SecurityDetail'
import { KIND_LABEL, runningJobFor } from './signals'
import { type SignalScore } from './tests'

/*
 * Les composants du score, en tuiles.
 *
 * # Une case par geste, et plus six cases écrites d'avance
 *
 * La grille rendait six tuiles fixes, une par signal. Un projet qui ne déclare
 * ni campagne E2E ni tests de charge en voyait donc deux affichant « non
 * mesuré » — une absence qui se lit comme un manque, alors que personne ne les
 * avait demandées.
 *
 * Elle rend maintenant **ce que le serveur envoie** : un composant par geste de
 * vérification déclaré. Ajouter un pipeline à l'automatisation fait apparaître sa
 * case ; le retirer la fait disparaître de la release suivante — mais elle reste
 * sur les précédentes, dont le plan est figé (doc 15 §15.7).
 *
 * # Ce que l'écran ne décide plus
 *
 * Le pipeline à relancer et la famille de rapports à ouvrir venaient de trois
 * tables écrites ici. Elles devenaient fausses dès qu'un projet déclarait deux
 * campagnes de même nature : elles ne relancent pas le même pipeline. Le serveur
 * les rend maintenant sur le composant, avec le geste qui les porte.
 *
 * La grammaire de la tuile — bordure colorée par le verdict, icône à gauche,
 * icône du verdict à droite, valeur en chiffres — reste celle de la maquette.
 */

/**
 * Le détail qu'une famille de rapports sait ouvrir.
 *
 * Sur la **famille** et non sur le signal : c'est elle qui range les rapports, et
 * c'est le serveur qui la rend maintenant sur chaque composant.
 */
function detailFor(family: string | undefined): 'reports' | 'security' | 'bench' | 'none' {
  switch (family) {
    case 'unit':
    case 'integration':
    case 'e2e':
    case 'static':
      return 'reports'
    case 'security':
      return 'security'
    case 'load':
      return 'bench'
    default:
      return 'none'
  }
}

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
        // La clé est celle du composant — le geste — et non le signal : deux
        // campagnes E2E le partagent, et React en confondrait les tuiles.
        <li key={signal.key}>
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

  const runningJob = runningJobFor(signal.ref, jobs)
  const running = runningJob !== undefined
  // Le pipeline à relancer vient du **geste**, et non d'une table indexée par
  // signal : deux campagnes E2E ne relancent pas le même.
  const kind = signal.ref
  // Chaque composant a son détail, et tous s'ouvrent en place : une famille de
  // tests montre ses cas, la sécurité ses constats, la performance ses
  // campagnes. Aucun ne mène plus à une page — perdre le score et les autres
  // composants pour lire une liste coûtait plus que ça ne rapportait.
  const family = signal.family
  const detail = detailFor(family)

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
          {signal.label || signal.signal}
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

  // Un composant sans détail reste une tuile inerte : un bouton qui n'ouvre rien
  // se clique une fois et déçoit.
  if (detail === 'none') return <div className={shell}>{body}</div>

  return (
    <>
      {/* La tuile entière est cliquable, sauf le bouton de relance qu'elle
          contient. Un `<button>` englobant l'aurait avalé — on ne peut pas
          imbriquer deux boutons. */}
      <div className={`relative ${shell}`}>
        <button
          type="button"
          onClick={() => setOpen(true)}
          aria-label={`Ouvrir le détail : ${signal.label || signal.signal}`}
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
          title={`${signal.label || signal.signal} — ${tag}`}
          onClose={() => setOpen(false)}
          size="lg"
        >
          {detail === 'reports' && family && (
            <FamilyDetail tag={tag} family={family} runName={runningJob?.run_name} />
          )}
          {detail === 'security' && <SecurityDetail tag={tag} runName={runningJob?.run_name} />}
          {/* La charge n'a pas de pipeline que le dashboard lance : ses campagnes
              viennent d'un runner extérieur (B16), d'où l'absence de frise. */}
          {detail === 'bench' && <BenchDetail tag={tag} />}
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
