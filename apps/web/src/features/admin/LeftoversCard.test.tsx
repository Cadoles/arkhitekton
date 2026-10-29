import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { render } from '../../test/render'
import { LeftoversCard } from './LeftoversCard'
import type { Leftover } from './installations'

/*
 * Ce que les plugins désinstallés ont laissé.
 */

function serve(leftovers: Leftover[]) {
  const calls: { url: string; method: string }[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: RequestInit) => {
      calls.push({ url, method: init?.method ?? 'GET' })
      if ((init?.method ?? 'GET') === 'GET') {
        return Promise.resolve(new Response(JSON.stringify({ leftovers }), { status: 200 }))
      }
      return Promise.resolve(new Response(JSON.stringify({ job: 9, plugin: 'deploy' }), { status: 202 }))
    }),
  )
  return calls
}

afterEach(() => vi.unstubAllGlobals())

describe('LeftoversCard', () => {
  it("ne purge qu'une fois l'identifiant du plugin retapé", async () => {
    const calls = serve([{ plugin: 'deploy', version: '2.0.1', table: 'goose_deploy_db_version' }])
    render(<LeftoversCard />)

    await userEvent.click(await screen.findByRole('button', { name: 'Purger' }))
    const confirm = screen.getAllByRole('button', { name: 'Purger' }).at(-1)
    if (!confirm) throw new Error('no confirmation')
    expect(confirm).toBeDisabled()

    await userEvent.type(screen.getByLabelText(/pour confirmer/), 'deploy')
    await userEvent.click(confirm)

    expect(await screen.findByText('Purge en cours')).toBeInTheDocument()
    const posted = calls.find((call) => call.method === 'POST')
    expect(posted?.url).toBe('/api/v1/admin/plugins/leftovers/deploy/purge')
  })

  it("ne montre rien quand aucun plugin n'a laissé de tables", async () => {
    const calls = serve([])
    const { container } = render(<LeftoversCard />)

    await vi.waitFor(() => expect(calls).toHaveLength(1))
    expect(container).toBeEmptyDOMElement()
  })
})
