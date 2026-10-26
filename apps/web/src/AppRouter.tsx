import { useEffect, useMemo, useState } from 'react'
import { RouterProvider, createBrowserRouter } from 'react-router'
import { Skeleton } from './components/primitives'
import { useSession } from './features/auth/session'
import { loadInterfaces } from './lib/runtime'
import { buildRoutes } from './routes'

/*
 * Le routeur, construit une fois les interfaces des plugins installés chargées.
 *
 * Leurs écrans sont des routes : un routeur construit avant eux ne les
 * connaîtrait pas, et une adresse qui y mène tomberait sur « page introuvable ».
 * Rendu sous le portail de session : `/me`, qui les liste, a déjà répondu.
 */
export function AppRouter() {
  const session = useSession()
  const interfaces = session.kind === 'authenticated' ? (session.identity.interfaces ?? []) : []
  const [loaded, setLoaded] = useState(interfaces.length === 0)

  useEffect(() => {
    if (loaded) return
    let cancelled = false
    void loadInterfaces(interfaces).then(() => {
      if (!cancelled) setLoaded(true)
    })
    return () => {
      cancelled = true
    }
    // Chargées une fois : une interface installée ensuite apparaît au rechargement de la page.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const router = useMemo(() => (loaded ? createBrowserRouter(buildRoutes()) : null), [loaded])
  if (!router) {
    return (
      <div className="mx-auto max-w-6xl px-4 py-6">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="mt-4 h-40 w-full" />
      </div>
    )
  }
  return <RouterProvider router={router} />
}
