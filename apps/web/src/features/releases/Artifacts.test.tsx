import { screen } from '@testing-library/react'
import { beforeEach, describe, expect, it } from 'vitest'
import { Artifacts } from './ReleaseScreen'
import { selectProject, setProjects } from '../../lib/project'
import { render } from '../../test/render'
import type { ReleaseDetail } from './api'

const release: ReleaseDetail = {
  tag: '1.0.0',
  kind: 'patch',
  state: 'ready',
  image_version: '1.0.0',
  created_by: 'vcarroy',
  created_at: '2026-11-04T10:00:00Z',
  binaries: [{ id: 7, name: 'app_1.0.0_linux_amd64.tar.gz', size_bytes: 2048 }],
}

describe('Artifacts', () => {
  beforeEach(() => {
    setProjects([{ slug: 'app', label: 'App' }])
    selectProject('app')
  })

  it('propose de télécharger les binaires de la release', () => {
    render(<Artifacts release={release} />)

    const link = screen.getByRole('link', { name: /app_1\.0\.0_linux_amd64\.tar\.gz/ })
    expect(link.getAttribute('href')).toBe('/api/v1/projects/app/releases/1.0.0/attachments/7?download=1')
  })
})
