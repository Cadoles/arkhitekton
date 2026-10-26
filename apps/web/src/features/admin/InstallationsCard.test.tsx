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
