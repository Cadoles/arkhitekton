import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { setProjects } from '../../lib/project'
import { render } from '../../test/render'
import { AttachmentList } from './AttachmentList'
import type { Attachment } from './tests'

/*
 * Les fichiers d'un résultat : chacun selon son type, et seulement les types
 * qu'un navigateur montre sans rien exécuter du fichier.
 */

const files: Attachment[] = [
  { id: 1, name: 'fails.png', media_type: 'image/png', size_bytes: 2048 },
  { id: 2, name: 'login.cy.js.mp4', media_type: 'video/mp4', size_bytes: 4_000_000 },
  { id: 3, name: 'report.html', media_type: 'text/html', size_bytes: 512 },
  { id: 4, name: 'logo.svg', media_type: 'image/svg+xml', size_bytes: 100 },
]

describe('AttachmentList', () => {
  setProjects([{ slug: 'mse', label: 'MSE' }])

  it('montre une capture et une vidéo depuis le projet, sous le rôle du lecteur', () => {
    const { container } = render(<AttachmentList tag="1.0.0" attachments={files} />)

    expect(screen.getByAltText('fails.png')).toHaveAttribute(
      'src',
      '/api/v1/projects/mse/releases/1.0.0/attachments/1',
    )
    const video = container.querySelector('video')
    expect(video).toHaveAttribute('src', '/api/v1/projects/mse/releases/1.0.0/attachments/2')
    expect(video).toHaveAttribute('preload', 'metadata')
  })

  // Un SVG peut porter du script : il ne s'affiche jamais, il se télécharge.
  it("n'affiche pas un SVG", () => {
    render(<AttachmentList tag="1.0.0" attachments={files} />)

    expect(screen.queryByAltText('logo.svg')).toBeNull()
    expect(screen.getAllByRole('link', { name: 'Télécharger' })).toHaveLength(4)
  })

  // Un rapport HTML s'ouvre à la demande, dans un cadre sans script ni accès à
  // l'origine du dashboard.
  it('ouvre un rapport HTML dans un cadre isolé', async () => {
    const { container } = render(<AttachmentList tag="1.0.0" attachments={files} />)
    expect(container.querySelector('iframe')).toBeNull()

    await userEvent.click(screen.getByRole('button', { name: 'Afficher' }))

    const frame = container.querySelector('iframe')
    expect(frame).toHaveAttribute('sandbox', '')
    expect(frame).toHaveAttribute('src', '/api/v1/projects/mse/releases/1.0.0/attachments/3')
  })
})
