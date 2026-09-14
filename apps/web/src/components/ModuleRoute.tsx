import type { ReactNode } from 'react'
import { Navigate } from 'react-router'
import { MODULE_MSE, useModule, useSession } from '../features/auth/session'
import { Skeleton } from './primitives'

/*
 * Les écrans qu'un module apporte, et la page d'accueil qui en dépend.
 *
 * Éteint, le module n'a pas d'écran : une URL qui y mène renvoie à l'accueil
 * plutôt que d'afficher un écran dont chaque requête répondrait 404 (doc 16).
 */

/** Rend `children` si le module est allumé, renvoie à l'accueil sinon. */
export function ModuleRoute({ module, children }: { module: string; children: ReactNode }) {
  const session = useSession()
  const enabled = useModule(module)

  if (session.kind === 'loading') return <Skeleton className="h-64 w-full" />
  if (!enabled) return <Navigate to="/releases" replace />
  return <>{children}</>
}

/**
 * La page d'accueil.
 *
 * Les environnements quand le plugin MSE est allumé — c'est la question qu'on
 * vient poser : qu'est-ce qui tourne où. Les releases sinon : c'est ce que le
 * noyau sait montrer sans lui.
 */
export function HomeRedirect() {
  const session = useSession()
  const mse = useModule(MODULE_MSE)

  if (session.kind === 'loading') return <Skeleton className="h-64 w-full" />
  return <Navigate to={mse ? '/environments' : '/releases'} replace />
}
