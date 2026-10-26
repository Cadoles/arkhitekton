import type { ReactNode } from 'react'
import { Navigate } from 'react-router'
import { useModule, useSession } from '../features/auth/session'
import { useActiveModules } from '../modules'
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
 * Celle du premier module allumé qui en propose une, ses environnements par
 * exemple. Les releases sinon : c'est ce que le noyau sait montrer sans module.
 */
export function HomeRedirect() {
  const session = useSession()
  const home = useActiveModules().find((module) => module.home)?.home

  if (session.kind === 'loading') return <Skeleton className="h-64 w-full" />
  return <Navigate to={home ?? '/releases'} replace />
}
