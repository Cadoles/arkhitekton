import type { ReactNode } from 'react'
import { Icon } from '../../components/Icon'
import { Card, ErrorState, Skeleton } from '../../components/primitives'
import { login } from '../../lib/api'
import { useSession } from './session'

/*
 * Portail d'entrée : rien ne s'affiche avant que l'identité soit connue.
 *
 * Les quatre issues appellent quatre réponses différentes, et les confondre est
 * la première façon de rendre une interface incompréhensible :
 *
 *   - en cours     → squelette, pas de saut de mise en page
 *   - anonyme      → connexion
 *   - sans rôle    → explication, et surtout pas une redirection vers une
 *                    connexion qui réussira à chaque fois sans rien changer
 *   - panne        → message et réessai
 */

export function SessionGate({ children }: { children: ReactNode }) {
  const session = useSession()

  if (session.kind === 'loading') {
    return (
      <div className="mx-auto max-w-6xl px-4 py-6">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="mt-4 h-40 w-full" />
      </div>
    )
  }

  if (session.kind === 'anonymous') {
    return (
      <Centered>
        <Card title="Connexion requise">
          <p className="mb-4 text-sm text-zinc-600">
            Le dashboard s'authentifie auprès de la forge. Vous serez redirigé, puis ramené ici.
          </p>
          <button
            type="button"
            onClick={() => login()}
            className="inline-flex items-center gap-2 rounded-md bg-zinc-900 px-3 py-2 text-sm font-medium text-white transition-colors hover:bg-zinc-700"
          >
            Se connecter
            <Icon name="arrow-right" className="size-4" />
          </button>
        </Card>
      </Centered>
    )
  }

  if (session.kind === 'unauthorized') {
    return (
      <Centered>
        <Card title="Compte sans habilitation">
          <p className="text-sm text-zinc-600">
            Votre compte est reconnu, mais aucun rôle ne lui est attribué. Un administrateur du
            dashboard doit vous en accorder un ; votre demande lui est déjà visible.
          </p>
        </Card>
      </Centered>
    )
  }

  if (session.kind === 'error') {
    return (
      <Centered>
        <ErrorState message={session.message} onRetry={() => window.location.reload()} />
      </Centered>
    )
  }

  return <>{children}</>
}

function Centered({ children }: { children: ReactNode }) {
  return <div className="mx-auto max-w-md px-4 py-16">{children}</div>
}
