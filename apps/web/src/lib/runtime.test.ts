import { afterEach, describe, expect, it, vi } from 'vitest'
import { exposeShared, loadInterfaces } from './runtime'
import { componentFor, registerComponent } from './components'
import { SHARED } from './shared'

/*
 * Le chargement des interfaces des plugins installés.
 */

afterEach(() => {
  document.head.innerHTML = ''
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('runtime', () => {
  it("expose au script d'un plugin le React et l'hôte du noyau", () => {
    exposeShared()
    const shared = window.__arkhitekton as Record<string, Record<string, unknown>>
    expect(typeof shared.react?.useState).toBe('function')
    expect(typeof shared.jsx?.jsx).toBe('function')
    expect(typeof shared.router?.Link).toBe('object')
    expect(typeof shared.host?.registerComponent).toBe('function')
    // Chaque nom que le preset de build donne au script d'un plugin est prêté.
    for (const name of Object.values(SHARED)) expect(shared[name]).toBeDefined()
  })

  it('charge les feuilles et les scripts, et attend que le script ait enregistré ses composants', async () => {
    const loading = loadInterfaces([
      { plugin: 'deploy', script: '/api/v1/plugin-interfaces/deploy/2.0.0/index.js', styles: ['/x.css'] },
    ])
    const script = document.head.querySelector('script')
    expect(script?.getAttribute('src')).toBe('/api/v1/plugin-interfaces/deploy/2.0.0/index.js')
    expect(document.head.querySelector('link')?.getAttribute('href')).toBe('/x.css')

    registerComponent('deploy/environments', () => null)
    script?.dispatchEvent(new Event('load'))
    await loading
    expect(componentFor('deploy/environments')).toBeDefined()
  })

  it("laisse de côté une interface qui échoue ou tarde, et le dit", async () => {
    vi.useFakeTimers()
    const logged = vi.spyOn(console, 'error').mockImplementation(() => {})
    const loading = loadInterfaces(
      [
        { plugin: 'broken', script: '/broken.js', styles: [] },
        { plugin: 'slow', script: '/slow.js', styles: [] },
      ],
      1000,
    )
    document.head.querySelector('script[src="/broken.js"]')?.dispatchEvent(new Event('error'))
    vi.advanceTimersByTime(1000)
    await loading
    expect(logged).toHaveBeenCalledTimes(2)
  })

  it("remplace un composant enregistré deux fois plutôt que de le doubler", () => {
    const first = () => null
    const second = () => null
    registerComponent('twice/c', first)
    registerComponent('twice/c', second)
    expect(componentFor('twice/c')).toBe(second)
  })
})
