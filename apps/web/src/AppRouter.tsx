import { useEffect, useMemo, useState } from 'react'
import { RouterProvider, createBrowserRouter } from 'react-router'
import { ErrorState, Skeleton } from './components/primitives'
import { useSession } from './features/auth/session'
import { InterfaceContext, loadInterface, setInterface, type UIInterface } from './lib/interface'
import { loadInterfaces } from './lib/runtime'
import { buildRoutes } from './routes'

/*
 * Le routeur, construit une fois l'interface composée et les scripts des
 * plugins installés chargés.
 *
 * Les écrans sont des routes : un routeur construit avant eux ne les
 * connaîtrait pas, et une adresse qui y mène tomberait sur « page introuvable ».
 * Rendu sous le portail de session : `/me`, qui donne le projet courant et les
 * scripts à charger, a déjà répondu.
 */
export function AppRouter() {
  const session = useSession()
  const [ui, setUI] = useState<UIInterface | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    if (session.kind !== 'authenticated') return
    let cancelled = false
    // Les composants des plugins s'enregistrent en chargeant leur script : il
    // le faut avant le premier rendu d'un bloc qui les nomme.
    void Promise.all([loadInterface(), loadInterfaces(session.identity.interfaces ?? [])])
      .then(([loaded]) => {
        if (cancelled) return
        setInterface(loaded)
        setUI(loaded)
      })
      .catch(() => {
        if (!cancelled) setFailed(true)
      })
    return () => {
      cancelled = true
    }
    // Chargée une fois : un plugin installé ensuite apparaît au rechargement de
    // la page, comme un changement de projet.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session.kind])

  const router = useMemo(() => (ui ? createBrowserRouter(buildRoutes(ui)) : null), [ui])
  if (failed) {
    return (
      <div className="mx-auto max-w-6xl px-4 py-6">
        <ErrorState message="L'interface n'a pas pu être chargée." onRetry={() => window.location.reload()} />
      </div>
    )
  }
  if (!router || !ui) {
    return (
      <div className="mx-auto max-w-6xl px-4 py-6">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="mt-4 h-40 w-full" />
      </div>
    )
  }
  return (
    <InterfaceContext.Provider value={ui}>
      <RouterProvider router={router} />
    </InterfaceContext.Provider>
  )
}
