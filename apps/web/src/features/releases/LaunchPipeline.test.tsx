import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { setProjects } from '../../lib/project'
import { mockFetch } from '../../test/http'
import { render } from '../../test/render'
import { LaunchPipelineButton } from './LaunchPipeline'
import { releaseSubject } from './tests'

const queue = {
  queues: [],
  kinds: [
    { kind: 'php/unit-tests', label: 'Tests unitaires', inputs: [{ name: 'revision', required: true, suggest: 'git-ref' }] },
    {
      kind: 'mse/e2e-tests', label: 'Tests E2E',
      inputs: [
        { name: 'revision', required: true, suggest: 'git-ref' },
        { name: 'environment', required: true, suggest: 'environment' },
      ],
    },
  ],
  suggestions: {},
  max_concurrent: 4,
}

describe("le lancement d'un pipeline sur une release", () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
    setProjects([{ slug: 'shop', label: 'Shop' }])
  })

  it('ne demande que ce que la release ne remplit pas, et lance sur la release', async () => {
    const calls = mockFetch([
      { path: '/me', body: { subject: 's', username: 'admin', role: 'admin', projects: [{ slug: 'shop', label: 'Shop' }] } },
      { path: '/queue', body: queue },
      { path: '/releases/1.8.0/run', method: 'POST', status: 202, body: { id: 9, kind: 'mse/e2e-tests' } },
    ])
    render(<LaunchPipelineButton subject={releaseSubject('1.8.0')} />)

    await userEvent.click(await screen.findByRole('button', { name: /Lancer un pipeline/ }))
    const menu = await screen.findByRole('combobox')
    // Le menu porte son fond et sa couleur : hérités, ils donnaient du blanc sur blanc en thème sombre.
    expect(menu.className).toContain('bg-white')
    expect(menu.className).toContain('text-zinc-900')

    await userEvent.selectOptions(menu, 'mse/e2e-tests')
    expect(screen.queryByText('revision')).not.toBeInTheDocument()
    await userEvent.type(screen.getByLabelText(/environment/), 'recette')
    await userEvent.click(screen.getByRole('button', { name: 'Lancer' }))

    const sent = calls.find((call) => call.method === 'POST' && call.url.includes('/projects/shop/releases/1.8.0/run'))
    expect(sent?.body).toEqual({ kind: 'mse/e2e-tests', params: { environment: 'recette' } })
  })

  it("se désactive sur un brouillon, qui n'a rien construit", async () => {
    mockFetch([
      { path: '/me', body: { subject: 's', username: 'admin', role: 'admin', projects: [{ slug: 'shop', label: 'Shop' }] } },
    ])
    render(<LaunchPipelineButton subject={releaseSubject('1.9.0')} disabled disabledReason="brouillon" />)

    expect(await screen.findByRole('button', { name: /Lancer un pipeline/ })).toBeDisabled()
  })
})
