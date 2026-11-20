import { useEffect, useRef, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Icon } from '../../components/Icon'
import { Card } from '../../components/primitives'
import { copyText } from '../../lib/clipboard'
import { useProbeProject, type Project } from './api'

/*
 * La mise en service d'un projet, d'un seul endroit.
 *
 * Trois étapes : le dashboard lit le dépôt, le worker prépare la clé de
 * déploiement, un humain la colle dans le dépôt. La carte suit les trois sans
 * qu'on revienne tester : elle relit le projet tant que la clé se prépare, et
 * revérifie le dépôt tant que la clé n'y est pas constatée. Le dashboard ne colle
 * pas la clé lui-même : écrire dans un dépôt reste un geste humain.
 */

/** Le projet est en service : son dépôt se lit et sa clé y est constatée. */
export function inService(project: Project): boolean {
  return project.check_ok === true && project.deploy_key_active
}

/** La revérification s'arrête après dix minutes : au-delà, un bouton la relance. */
const PROBE_EVERY = 10_000
const PROBE_FOR = 10 * 60_000
const RELOAD_EVERY = 3_000

export function SetupCard({ project }: { project: Project }) {
  const queryClient = useQueryClient()
  const probe = useProbeProject()
  const { mutate } = probe
  // La carte se montre si le projet n'était pas en service à l'arrivée, et le reste
  // pour dire qu'il l'est devenu.
  const [shown] = useState(() => !inService(project))
  const [probingSince, setProbingSince] = useState(() => Date.now())
  const [now, setNow] = useState(() => Date.now())

  const accessOK = project.check_ok === true
  const keyReady = Boolean(project.deploy_key)
  const keyActive = project.deploy_key_active
  const ready = inService(project)
  const probing = shown && !ready && keyReady && accessOK && now - probingSince < PROBE_FOR

  // Un projet jamais vérifié (déclaré par la configuration, par exemple) l'est à l'arrivée.
  const asked = useRef(false)
  useEffect(() => {
    if (shown && project.checked_at === undefined && !asked.current) {
      asked.current = true
      mutate(project.id)
    }
  }, [shown, project.checked_at, project.id, mutate])

  // La clé se prépare dans le worker : relire le projet jusqu'à ce qu'elle arrive.
  useEffect(() => {
    if (!shown || keyReady || project.deploy_key_error) return
    const timer = window.setInterval(() => {
      void queryClient.invalidateQueries({ queryKey: ['projects'] })
    }, RELOAD_EVERY)
    return () => window.clearInterval(timer)
  }, [shown, keyReady, project.deploy_key_error, queryClient])

  // La clé attend d'être collée : revérifier le dépôt, pendant dix minutes.
  useEffect(() => {
    if (!probing) return
    const timer = window.setInterval(() => {
      setNow(Date.now())
      mutate(project.id)
    }, PROBE_EVERY)
    return () => window.clearInterval(timer)
  }, [probing, mutate, project.id])

  if (!shown) return null
  if (ready) {
    return (
      <p className="flex items-center gap-1.5 rounded-md border border-healthy-500 bg-healthy-50 px-3 py-2 text-sm text-healthy-700">
        <Icon name="check" className="size-4" />
        Projet en service : son dépôt se lit et sa clé de déploiement y est en place.
      </p>
    )
  }

  const recheck = () => {
    setProbingSince(Date.now())
    setNow(Date.now())
    probe.mutate(project.id)
  }

  return (
    <Card title="Mise en service">
      <ol className="flex flex-col gap-3">
        <Step
          state={project.checked_at === undefined || (probe.isPending && !accessOK) ? 'pending' : accessOK ? 'done' : 'failed'}
          title="Accès au dépôt"
        >
          {accessOK ? (
            <span>{project.check_note}</span>
          ) : project.checked_at === undefined ? (
            <span>Vérification…</span>
          ) : (
            <>
              <span>{project.check_note || 'Le dépôt ne se lit pas.'}</span>
              <RecheckButton pending={probe.isPending} onClick={recheck} />
            </>
          )}
        </Step>

        <Step
          state={keyReady ? 'done' : project.deploy_key_error ? 'failed' : 'pending'}
          title="Clé de déploiement préparée"
        >
          {keyReady ? (
            <span>Préparée par le dashboard.</span>
          ) : project.deploy_key_error ? (
            <span>{project.deploy_key_error}. Le worker réessaie chaque minute.</span>
          ) : (
            <span>En préparation, moins d&apos;une minute.</span>
          )}
        </Step>

        <Step
          state={keyActive ? 'done' : keyReady && accessOK ? 'pending' : 'waiting'}
          title="Clé en place dans le dépôt"
        >
          {keyReady && project.deploy_key ? (
            <div className="flex flex-col gap-1.5">
              <span>
                Collez-la dans les clés de déploiement du dépôt ; la lecture seule suffit.
              </span>
              <CopyableKey value={project.deploy_key} />
              {project.keys_url && (
                <a
                  href={project.keys_url}
                  target="_blank"
                  rel="noreferrer noopener"
                  className="inline-flex w-fit items-center gap-1 text-sm text-zinc-900 underline"
                >
                  <Icon name="external" className="size-3.5" />
                  Ouvrir les clés de déploiement du dépôt
                </a>
              )}
              {project.key_note && <span className="text-xs text-zinc-500">{project.key_note}</span>}
              {probing ? (
                <span className="text-xs text-zinc-500">
                  Vérification automatique : la carte se met à jour dès que la clé est constatée.
                </span>
              ) : (
                accessOK && <RecheckButton pending={probe.isPending} onClick={recheck} />
              )}
            </div>
          ) : (
            <span>Après les deux étapes précédentes.</span>
          )}
        </Step>
      </ol>
    </Card>
  )
}

type StepState = 'done' | 'pending' | 'failed' | 'waiting'

const STEP: Record<StepState, { icon: 'check' | 'clock' | 'cross' | 'minus'; tone: string }> = {
  done: { icon: 'check', tone: 'text-healthy-700' },
  pending: { icon: 'clock', tone: 'text-warn-700' },
  failed: { icon: 'cross', tone: 'text-critical-700' },
  waiting: { icon: 'minus', tone: 'text-zinc-400' },
}

function Step({ state, title, children }: { state: StepState; title: string; children: ReactNode }) {
  return (
    <li className="flex gap-2" aria-label={`${title} : ${LABEL[state]}`}>
      <Icon name={STEP[state].icon} className={`mt-0.5 size-4 shrink-0 ${STEP[state].tone}`} />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5 text-sm text-zinc-700">
        <span className="font-medium text-zinc-900">{title}</span>
        {children}
      </div>
    </li>
  )
}

const LABEL: Record<StepState, string> = {
  done: 'fait',
  pending: 'en cours',
  failed: 'en échec',
  waiting: 'en attente',
}

function RecheckButton({ pending, onClick }: { pending: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      disabled={pending}
      onClick={onClick}
      className="w-fit rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-50"
    >
      {pending ? 'Vérification…' : 'Revérifier'}
    </button>
  )
}

function CopyableKey({ value }: { value: string }) {
  const [state, setState] = useState<'idle' | 'copied' | 'failed'>('idle')
  return (
    <div className="flex items-start gap-2">
      <code className="min-w-0 flex-1 break-all rounded-md border border-zinc-200 bg-zinc-100 px-2 py-1 font-mono text-[11px] text-zinc-900">
        {value}
      </code>
      <button
        type="button"
        onClick={() => void copyText(value).then((done) => setState(done ? 'copied' : 'failed'))}
        className="flex shrink-0 items-center gap-1 rounded-md border border-zinc-300 bg-white px-2 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
      >
        <Icon name={state === 'copied' ? 'check' : 'link'} className="size-3.5" />
        {state === 'copied' ? 'Copiée' : state === 'failed' ? 'Copie refusée' : 'Copier'}
      </button>
    </div>
  )
}
