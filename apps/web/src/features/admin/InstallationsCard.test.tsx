import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { render } from '../../test/render'
import { InstallationsCard } from './InstallationsCard'
import type { Installation } from './installations'

/*
 * Les plugins installés depuis un registre.
 */

const installed: Installation[] = [
  {
    id: 2,
    reference: 'reg.example/plugins/gotest@sha256:aa',
    digest: 'sha256:aa',
    plugin: 'gotest',
    version: '1.0.0',
    signed_by: 'sha256:key',
    state: 'installed',
    active: true,
    requested_by: 'admin',
    requested_at: '2026-10-23T10:00:00Z',
  },
  {
    id: 3,
    reference: 'reg.example/plugins/gotest@sha256:bb',
    digest: 'sha256:bb',
    plugin: 'gotest',
    version: '1.0.1',
    state: 'installed',
    active: false,
    requested_by: 'admin',
    requested_at: '2026-10-23T11:00:00Z',
  },
  {
    id: 4,
    reference: 'reg.example/plugins/other@sha256:cc',
    digest: 'sha256:cc',
    state: 'failed',
    error: 'signed by no trusted key',
    active: false,
    requested_by: 'admin',
    requested_at: '2026-10-23T12:00:00Z',
  },
]

/** Tirée, mais d'une source que l'instance ne reconnaît pas : elle attend une décision. */
const unrecognised: Installation = {
  id: 6,
  reference: 'reg.example/plugins/junit@sha256:dd',
  digest: 'sha256:dd',
  plugin: 'junit',
  state: 'unrecognised',
  error: 'signed by no trusted key',
  active: false,
  requested_by: 'admin',
  requested_at: '2026-10-30T09:00:00Z',
}

function serve(installations: Installation[]) {
  const calls: { url: string; method: string; body?: string }[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: RequestInit) => {
      calls.push({
        url,
        method: init?.method ?? 'GET',
        body: init?.body as string | undefined,
      })
      if ((init?.method ?? 'GET') === 'GET') {
        return Promise.resolve(new Response(JSON.stringify({ installations }), { status: 200 }))
      }
      return Promise.resolve(new Response(JSON.stringify({ id: 5, job: 9 }), { status: 202 }))
    }),
  )
  return calls
}

afterEach(() => vi.unstubAllGlobals())

describe('InstallationsCard', () => {
  it("dit pourquoi une installation a échoué, et n'active que ce qui est installé", async () => {
    serve(installed)
    render(<InstallationsCard />)

    expect(await screen.findByText('signed by no trusted key')).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: 'Activer' })).toHaveLength(1)
    expect(screen.getByRole('button', { name: 'Désinstaller' })).toBeInTheDocument()
  })

  it("dit qu'une installation n'est pas signée plutôt que d'afficher un signataire", async () => {
    const [first] = installed
    if (!first) throw new Error('no installation to sign')
    serve([{ ...first, signed_by: 'unsigned' }])
    render(<InstallationsCard />)

    expect(await screen.findByText(/Non signé/)).toBeInTheDocument()
    expect(screen.queryByText('unsigned')).not.toBeInTheDocument()
  })

  it('avertit sur une source non reconnue et propose de l\'accepter', async () => {
    const calls = serve([unrecognised])
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    render(<InstallationsCard />)

    expect(await screen.findByText(/ne vient pas d'une source reconnue/)).toBeInTheDocument()
    // Pas de bouton d'activation : il n'y a rien d'installé à activer.
    expect(screen.queryByRole('button', { name: 'Activer' })).not.toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: 'Accepter la source' }))

    const posted = calls.find((call) => call.method === 'POST')
    expect(posted?.url).toBe('/api/v1/admin/plugins/installations/6/accept')
  })

  // L'acceptation est une décision : rien n'est envoyé si elle n'est pas confirmée.
  it("n'accepte rien sans confirmation", async () => {
    const calls = serve([unrecognised])
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    render(<InstallationsCard />)

    await userEvent.click(await screen.findByRole('button', { name: 'Accepter la source' }))
    expect(calls.find((call) => call.method === 'POST')).toBeUndefined()
  })

  it('demande une installation par sa référence', async () => {
    const calls = serve([])
    render(<InstallationsCard />)

    await userEvent.type(
      await screen.findByLabelText('Référence du plugin'),
      'reg.example/plugins/gotest@sha256:aa',
    )
    await userEvent.click(screen.getByRole('button', { name: 'Installer' }))

    const posted = calls.find((call) => call.method === 'POST')
    expect(posted?.url).toBe('/api/v1/admin/plugins/installations')
    expect(JSON.parse(posted?.body ?? '{}')).toEqual({
      reference: 'reg.example/plugins/gotest@sha256:aa',
    })
  })
})
