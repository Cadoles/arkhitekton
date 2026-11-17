import { screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { render } from '../../test/render'
import type { Project } from './api'
import { ProjectKey } from './ProjectForm'

/*
 * La clé de déploiement d'un projet, quand le worker n'a pas pu la poser.
 */

const project = {
  id: 8, slug: 'mstore', uid: '3f2b9c1e-7a4d-4e8b-9c0a-5d6e7f8a9b0c', deploy_key_active: false,
} as unknown as Project

describe('ProjectKey', () => {
  it('dit pourquoi le worker n’a pas pu poser la clé, au lieu de la promettre', () => {
    render(<ProjectKey project={{ ...project, deploy_key_error: 'a Secret already carries the name of this project’s key' }} />)

    expect(screen.getByText(/La clé n'a pas pu être posée : a Secret already carries/)).toBeInTheDocument()
    expect(screen.queryByText(/sera posée au prochain passage/)).not.toBeInTheDocument()
  })

  it('promet la clé au prochain passage tant que rien n’a échoué', () => {
    render(<ProjectKey project={project} />)

    expect(screen.getByText(/sera posée au prochain passage/)).toBeInTheDocument()
  })
})
