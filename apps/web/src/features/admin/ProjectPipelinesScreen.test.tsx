import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Outlet, Route, Routes } from 'react-router'
import { mockFetch } from '../../test/http'
import { render } from '../../test/render'
import { ProjectPipelinesScreen } from './ProjectTabs'
import type { Project } from './api'

const project = { slug: 'shop', label: 'Shop' } as Project

describe("les pipelines d'un projet", () => {
  beforeEach(() => vi.unstubAllGlobals())

  it('se retiennent ou se retirent par leur interrupteur', async () => {
    const calls = mockFetch([
      { path: '/projects/shop/pipelines/php%2Funit-tests', method: 'PUT', body: { kind: 'php/unit-tests', enabled: false } },
      {
        path: '/projects/shop/pipelines',
        body: { pipelines: [{ kind: 'php/unit-tests', label: 'Tests unitaires', enabled: true, verification: true }] },
      },
    ])
    render(
      <Routes>
        <Route element={<Outlet context={project} />}>
          <Route path="*" element={<ProjectPipelinesScreen />} />
        </Route>
      </Routes>,
    )

    const toggle = await screen.findByRole('switch', { name: 'Retenir Tests unitaires pour ce projet' })
    expect(toggle).toHaveAttribute('aria-checked', 'true')
    await userEvent.click(toggle)

    expect(calls.find((call) => call.method === 'PUT')?.body).toEqual({ enabled: false })
  })
})
