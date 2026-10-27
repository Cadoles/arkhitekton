import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import { render } from '../test/render'
import { Markdown } from './Markdown'

describe('le texte mis en forme', () => {
  it('rend paragraphes, listes, emphase, code et liens sûrs', () => {
    render(
      <Markdown
        text={'**Gras** et *italique* avec `code`.\n\n- [écran](/releases)\n- [site](https://example.org)\n- [piège](javascript:alert(1))'}
      />,
    )
    expect(screen.getByText('Gras').tagName).toBe('STRONG')
    expect(screen.getByText('italique').tagName).toBe('EM')
    expect(screen.getByText('code').tagName).toBe('CODE')
    expect(screen.getByRole('link', { name: 'écran' })).toHaveAttribute('href', '/releases')
    expect(screen.getByRole('link', { name: 'site' })).toHaveAttribute('rel', 'noreferrer noopener')
    expect(screen.queryByRole('link', { name: 'piège' })).not.toBeInTheDocument()
    expect(screen.getAllByRole('listitem')).toHaveLength(3)
  })

  it("n'interprète pas de HTML", () => {
    const { container } = render(<Markdown text={'<img src=x onerror=alert(1)>'} />)
    expect(container.querySelector('img')).toBeNull()
  })
})
