import { QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { testQueryClient } from '../../test/render'
import { browser, type AgentRequest } from './api'
import { ConsentScreen } from './ConsentScreen'

/*
 * Le consentement donné à un agent IA.
 */

function serve(request: AgentRequest | null) {
  const posted: { url: string; body: unknown }[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: RequestInit) => {
      if (url === '/api/v1/me') {
        return Promise.resolve(new Response(JSON.stringify({ subject: 's', username: 'alice', role: 'operator' })))
      }
      if ((init?.method ?? 'GET') === 'POST') {
        posted.push({ url, body: JSON.parse(String(init?.body)) })
        return Promise.resolve(new Response(JSON.stringify({ redirect: 'http://127.0.0.1:4000/cb?code=c' })))
      }
      if (!request) {
        return Promise.resolve(new Response(JSON.stringify({ title: 'Unknown or expired request' }), { status: 404 }))
      }
      return Promise.resolve(new Response(JSON.stringify(request)))
    }),
  )
  return posted
}

function show() {
  return render(
    <QueryClientProvider client={testQueryClient()}>
      <MemoryRouter initialEntries={['/connect/r1']}>
        <Routes>
          <Route path="/connect/:request" element={<ConsentScreen />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

const asked: AgentRequest = {
  id: 'r1',
  client: 'Claude Code',
  redirect_host: '127.0.0.1:4000',
  scopes: ['dashboard:read', 'dashboard:run'],
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('ConsentScreen', () => {
  it("autorise l'agent à lire seulement quand on décoche l'action, puis le renvoie à l'agent", async () => {
    const posted = serve(asked)
    const leave = vi.spyOn(browser, 'leaveTo').mockImplementation(() => {})
    show()

    expect(await screen.findByText('Claude Code')).toBeInTheDocument()
    expect(screen.getByText('127.0.0.1:4000')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('checkbox'))
    await userEvent.click(screen.getByRole('button', { name: 'Autoriser' }))

    await vi.waitFor(() => expect(leave).toHaveBeenCalledWith('http://127.0.0.1:4000/cb?code=c'))
    expect(posted).toEqual([{ url: '/api/v1/oauth/requests/r1/approve', body: { scopes: ['dashboard:read'] } }])
  })

  it("ne propose pas d'agir à un agent qui ne demande qu'à lire, et refuse", async () => {
    const posted = serve({ ...asked, scopes: ['dashboard:read'] })
    vi.spyOn(browser, 'leaveTo').mockImplementation(() => {})
    show()

    await screen.findByText('Claude Code')
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Refuser' }))
    await vi.waitFor(() => expect(posted.map((call) => call.url)).toEqual(['/api/v1/oauth/requests/r1/deny']))
  })

  it("dit qu'une demande expirée se relance depuis l'agent", async () => {
    serve(null)
    show()
    expect(await screen.findByText(/relancez la connexion depuis l’agent/)).toBeInTheDocument()
  })
})
