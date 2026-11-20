import { act, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mockFetch } from '../../test/http'
import { render } from '../../test/render'
import { useProjects, type Project } from './api'
import { SetupCard } from './SetupCard'

/*
 * La mise en service d'un projet : trois étapes suivies sans qu'on revienne tester.
 */

const base = {
  id: 8, slug: 'app', repository: 'team/app', label: 'App', uid: 'u', deploy_key_active: false,
  checked_at: '2026-11-20T10:00:00Z', check_ok: true, check_note: 'default branch: main',
} as unknown as Project

const withKey = {
  ...base,
  deploy_key: 'ssh-ed25519 AAAA arkhitekton',
  keys_url: 'https://forge/team/app/settings/keys',
  key_note: "the project's key is not among the repository's 0 deploy key(s)",
} as Project

/** La page d'un projet, réduite à ce que la carte en attend : la liste lue. */
function Page() {
  const { data } = useProjects()
  const project = data?.projects[0]
  return project ? <SetupCard project={project} /> : null
}

function step(name: string) {
  return screen.getByRole('listitem', { name: new RegExp(`^${name} : `) })
}

describe('la mise en service', () => {
  beforeEach(() => vi.unstubAllGlobals())
  afterEach(() => vi.useRealTimers())

  it('dit où en est chaque étape, et où coller la clé', () => {
    mockFetch([])
    render(<SetupCard project={withKey} />)

    expect(step('Accès au dépôt')).toHaveAccessibleName('Accès au dépôt : fait')
    expect(step('Clé de déploiement préparée')).toHaveAccessibleName('Clé de déploiement préparée : fait')
    expect(step('Clé en place dans le dépôt')).toHaveAccessibleName('Clé en place dans le dépôt : en cours')
    expect(screen.getByRole('link', { name: /Ouvrir les clés de déploiement du dépôt/ })).toHaveAttribute(
      'href',
      'https://forge/team/app/settings/keys',
    )
    expect(screen.getByText(/not among the repository's 0 deploy key/)).toBeInTheDocument()
  })

  it('attend la clé du worker en relisant le projet', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const calls = mockFetch([{ path: '/projects', body: { projects: [base] } }])
    render(<Page />)

    expect(await screen.findByRole('listitem', { name: 'Clé de déploiement préparée : en cours' })).toBeInTheDocument()
    const reads = () => calls.filter((call) => call.method === 'GET' && call.url.endsWith('/projects')).length
    const before = reads()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3_100)
    })
    expect(reads()).toBeGreaterThan(before)
  })

  it('revérifie seule le dépôt tant que la clé n’y est pas, sans clic', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const calls = mockFetch([
      { path: '/projects/8/probe', method: 'POST', body: { ok: true, note: '', key_present: false, key_note: '' } },
      { path: '/projects', body: { projects: [withKey] } },
    ])
    render(<SetupCard project={withKey} />)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_100)
    })
    expect(calls.filter((call) => call.method === 'POST' && call.url.includes('/probe'))).toHaveLength(1)
    expect(screen.getByText(/Vérification automatique/)).toBeInTheDocument()
  })

  it('dit que le projet est en service une fois la clé constatée', () => {
    mockFetch([])
    const { rerender } = render(<SetupCard project={withKey} />)
    rerender(<SetupCard project={{ ...withKey, deploy_key_active: true }} />)

    expect(screen.getByText(/Projet en service/)).toBeInTheDocument()
  })

  it('ne se montre pas pour un projet déjà en service', () => {
    mockFetch([])
    const { container } = render(<SetupCard project={{ ...withKey, deploy_key_active: true }} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('propose de revérifier un dépôt qui ne se lit pas', async () => {
    const calls = mockFetch([
      { path: '/projects/8/probe', method: 'POST', body: { ok: false, note: '404', key_present: null, key_note: '' } },
      { path: '/projects', body: { projects: [] } },
    ])
    render(<SetupCard project={{ ...base, check_ok: false, check_note: 'gitea: 404 on /repos/team/app' }} />)

    expect(step('Accès au dépôt')).toHaveAccessibleName('Accès au dépôt : en échec')
    expect(screen.getByText(/404 on/)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Revérifier' }))
    expect(calls.some((call) => call.method === 'POST' && call.url.includes('/projects/8/probe'))).toBe(true)
  })
})
