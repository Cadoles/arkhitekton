import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import './styles/theme.css'
import { AppRouter } from './AppRouter'
import { SessionGate } from './features/auth/SessionGate'
import { ApiError } from './lib/api'
import { exposeShared } from './lib/runtime'
import { registerCoreComponents } from './screens/core'

// Avant tout : le script d'un plugin installé prend React et le reste ici.
exposeShared()
registerCoreComponents()

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // Réessayer un 401, un 403 ou un 404 ne fait que retarder l'affichage du
      // message qui explique la situation.
      retry: (failureCount, error) => {
        if (error instanceof ApiError && error.status < 500) return false
        return failureCount < 2
      },
      // Le dashboard croise des sources rafraîchies à des rythmes sans rapport.
      // Un intervalle global serait faux partout : chaque requête pose le sien.
      refetchOnWindowFocus: false,
    },
  },
})

const container = document.getElementById('root')
if (!container) throw new Error('élément #root introuvable')

createRoot(container).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      {/* Le portail encadre le routeur : aucune route ne s'affiche avant que
          l'identité soit connue, ce qui évite qu'un écran se peuple puis se vide
          quand /me répond 401. */}
      <SessionGate>
        <AppRouter />
      </SessionGate>
    </QueryClientProvider>
  </StrictMode>,
)
