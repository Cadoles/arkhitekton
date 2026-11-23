import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { render } from '../../test/render'
import type { Agents } from './api'
import { AgentsScreen } from './AgentsScreen'

/*
 * Les agents IA autorisés.
 */

function serve(agents: Agents) {
  const deleted: string[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: RequestInit) => {
      if (init?.method === 'DELETE') {
        deleted.push(url)
        return Promise.resolve(new Response(null, { status: 204 }))
      }
      return Promise.resolve(new Response(JSON.stringify(agents)))
    }),
  )
  return deleted
}

afterEach(() => vi.unstubAllGlobals())

const url = 'http://dashboard.arkhitekton.local/mcp'

describe('AgentsScreen', () => {
  it("donne l'adresse et la commande pour brancher un agent, et révoque un agent après confirmation", async () => {
    const deleted = serve({
      mcp_url: url,
      all: false,
      agents: [
        { id: 3, name: 'Claude Code', scopes: ['dashboard:read', 'dashboard:run'], created_at: '2026-11-23T10:00:00Z' },
      ],
    })
    render(<AgentsScreen />)

    expect(await screen.findByText(url)).toBeInTheDocument()
    expect(screen.getByText(`claude mcp add --transport http arkhitekton ${url}`)).toBeInTheDocument()
    expect(screen.getByText('Vos agents autorisés')).toBeInTheDocument()
    expect(screen.getByText('consulte et agit')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: 'Révoquer' }))
    const dialog = screen.getByRole('dialog')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Révoquer' }))
    await vi.waitFor(() => expect(deleted).toEqual(['/api/v1/agents/3']))
  })

  it("donne la marche à suivre de l'agent choisi, avec l'adresse du serveur", async () => {
    serve({ mcp_url: url, all: false, agents: [] })
    render(<AgentsScreen />)

    await userEvent.click(await screen.findByRole('button', { name: 'Cursor' }))
    expect(screen.getByRole('button', { name: 'Cursor' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByText(/"mcpServers"/)).toHaveTextContent(`"url": "${url}"`)

    await userEvent.click(screen.getByRole('button', { name: 'Codex' }))
    expect(screen.getByText(`codex mcp add arkhitekton --url ${url}`)).toBeInTheDocument()
    expect(screen.getByText('codex mcp login arkhitekton')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: 'Pi' }))
    expect(screen.getByText('pi mcp login arkhitekton')).toBeInTheDocument()
  })

  it('montre à un administrateur les agents de chacun', async () => {
    serve({
      mcp_url: url,
      all: true,
      agents: [{ id: 4, name: 'Cursor', owner: 'bob', scopes: ['dashboard:read'], created_at: '2026-11-23T10:00:00Z' }],
    })
    render(<AgentsScreen />)

    expect(await screen.findByText('Agents autorisés')).toBeInTheDocument()
    expect(screen.getByText('de bob')).toBeInTheDocument()
    expect(screen.getByText('consulte')).toBeInTheDocument()
  })
})
