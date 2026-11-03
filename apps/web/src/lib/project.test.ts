import { afterEach, describe, expect, it } from 'vitest'
import { currentProject, setProjects } from './project'

const projects = [
  { slug: 'arkhitekton', label: 'Arkhitekton' },
  { slug: 'mstore', label: 'Mstore' },
]

describe('setProjects', () => {
  afterEach(() => {
    window.history.replaceState(null, '', '/')
  })

  it("retient le projet qu'un lien nomme", () => {
    window.history.replaceState(null, '', '/releases/1.0.0?project=mstore')
    setProjects(projects)
    expect(currentProject()).toBe('mstore')
  })

  it('ignore un projet inconnu', () => {
    window.history.replaceState(null, '', '/releases/1.0.0?project=ailleurs')
    setProjects(projects)
    expect(currentProject()).toBe('arkhitekton')
  })
})
