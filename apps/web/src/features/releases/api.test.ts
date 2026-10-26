import { describe, expect, it } from 'vitest'

import { setProjects } from '../../lib/project'
import { downloadURL } from './api'

describe('downloadURL', () => {
  it('passe par le projet courant, seule route qui sert un livrable', () => {
    setProjects([{ slug: 'mstore', label: 'Mstore' }])
    expect(downloadURL('v1.2.0', 7)).toBe('/api/v1/projects/mstore/releases/v1.2.0/exports/7/download')
  })
})
