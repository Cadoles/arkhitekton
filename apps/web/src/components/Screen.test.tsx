import { describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import { render } from '../test/render'
import { ScreenView, Slot } from './Screen'
import { registerComponent, type BlockComponentProps } from '../lib/components'

function Header({ title }: BlockComponentProps) {
  return (
    <header>
      <h1>{String(title)}</h1>
      <Slot name="badges" context={{ tag: '3.4.0' }} />
    </header>
  )
}

registerComponent('test/header', Header)
registerComponent('test/badge', ({ tag }) => <span>pastille de {String(tag)}</span>)
registerComponent('test/broken', () => {
  throw new Error('en panne')
})

describe("le rendu d'un écran", () => {
  it("rend les blocs dans l'ordre, et ce que d'autres ont ajouté à leurs emplacements", () => {
    render(
      <ScreenView
        screen={{
          name: 'release',
          blocks: [
            {
              id: 'core.header',
              component: 'test/header',
              props: { title: 'Release' },
              slots: { badges: [{ id: 'mse.deployed', component: 'test/badge' }] },
            },
            { id: 'core.notes', component: 'test/header', props: { title: 'Notes' } },
          ],
        }}
      />,
    )
    expect(screen.getAllByRole('heading').map((heading) => heading.textContent)).toEqual(['Release', 'Notes'])
    expect(screen.getByText('pastille de 3.4.0')).toBeInTheDocument()
  })

  it("signale à sa place un bloc en panne ou inconnu, et rend le reste", () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    render(
      <ScreenView
        screen={{
          name: 'release',
          blocks: [
            { id: 'plugin.broken', component: 'test/broken' },
            { id: 'plugin.absent', component: 'test/absent' },
            { id: 'core.header', component: 'test/header', props: { title: 'Toujours là' } },
          ],
        }}
      />,
    )
    expect(screen.getByText(/« plugin.broken » n'a pas pu s'afficher/)).toBeInTheDocument()
    expect(screen.getByText(/Composant inconnu : test\/absent/)).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Toujours là' })).toBeInTheDocument()
  })
})
