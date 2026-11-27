import { afterEach, describe, expect, it } from 'vitest'
import { currentProject, setProjects } from './project'

const projects = [
  { slug: 'arkhitekton', label: 'Arkhitekton' },
  { slug: 'shop', label: 'Shop' },
]

describe('setProjects', () => {
  afterEach(() => {
    window.history.replaceState(null, '', '/')
    window.localStorage?.clear()
  })

  it("retient le projet qu'un lien nomme", () => {
    window.history.replaceState(null, '', '/releases/1.0.0?project=shop')
    setProjects(projects)
    expect(currentProject()).toBe('shop')
  })

  it('ignore un projet inconnu', () => {
    window.history.replaceState(null, '', '/releases/1.0.0?project=ailleurs')
    setProjects(projects)
    expect(currentProject()).toBe('arkhitekton')
  })
})
