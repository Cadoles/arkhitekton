import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { render } from '../../test/render'
import { PluginCard } from './PluginCard'
import type { Plugin } from './plugins'

/*
 * Le formulaire d'un plugin.
 *
 * Il reçoit des clés d'API depuis un navigateur : les tests portent d'abord sur
 * ce qui ne doit **jamais** apparaître, puis sur la façon dont on perdrait une
 * valeur sans s'en apercevoir.
 */

const redmine: Plugin = {
  id: 'redmine',
  kind: 'tracker',
  label: 'Redmine',
  kind_label: 'Suivi',
  summary: 'Versions et demandes.',
  per_project: true,
  fields: [
    { name: 'url', label: "URL de l'instance", type: 'url' },
    { name: 'api_key', label: "Clé d'API", type: 'text', secret: true },
  ],
  enabled: true,
  config: { url: 'https://redmine.example.fr' },
  secrets_set: ['api_key'],
  secrets_configured: true,
}

describe('PluginCard', () => {
  // La valeur d'un champ sensible n'est jamais rendue par l'API, donc jamais
  // affichée. L'écran n'en connaît que la présence — c'est ce qui rend la saisie
  // d'une clé depuis un navigateur acceptable.
  it("n'affiche jamais la valeur d'un champ sensible, seulement sa présence", () => {
    render(<PluginCard plugin={redmine} scope="global" saving={false} onSave={vi.fn()} />)

    const cle = screen.getByLabelText("Clé d'API")
    expect(cle).toHaveValue('')
    expect(cle).toHaveAttribute('placeholder', expect.stringContaining('enregistrée'))
  })

  // Le piège que ce test existe pour éviter : l'écran ne reçoit jamais la clé,
  // donc ne peut pas la renvoyer. La soumettre vide l'effacerait — et modifier
  // une URL suffirait alors à couper la synchronisation.
  it('soumet un champ sensible vide sans demander son effacement', async () => {
    const onSave = vi.fn()
    render(<PluginCard plugin={redmine} scope="global" saving={false} onSave={onSave} />)

    await userEvent.click(screen.getByRole('button', { name: 'Enregistrer' }))

    expect(onSave).toHaveBeenCalledTimes(1)
    const draft = onSave.mock.calls[0]![0]
    expect(draft.config.api_key ?? '').toBe('')
    expect(draft.cleared_secrets).toBeUndefined()
  })

  // L'effacement existe, et demande un geste distinct de « laisser vide ».
  it("demande explicitement l'effacement d'un champ sensible", async () => {
    const onSave = vi.fn()
    render(<PluginCard plugin={redmine} scope="global" saving={false} onSave={onSave} />)

    await userEvent.click(screen.getByRole('button', { name: 'Effacer' }))
    await userEvent.click(screen.getByRole('button', { name: 'Enregistrer' }))

    expect(onSave.mock.calls[0]![0].cleared_secrets).toEqual(['api_key'])
  })

  // Sans scellement configuré, le champ est désactivé plutôt que laissé saisir :
  // le serveur refuserait l'enregistrement, et découvrir le refus après avoir
  // tapé une clé de production est le pire moment pour l'apprendre.
  it('désactive les champs sensibles quand le scellement manque', () => {
    render(
      <PluginCard
        plugin={{ ...redmine, secrets_configured: false }}
        scope="global"
        saving={false}
        onSave={vi.fn()}
      />,
    )

    expect(screen.getByLabelText("Clé d'API")).toBeDisabled()
    expect(screen.getByText(/SECRETS_KEY/)).toBeInTheDocument()
  })

  // Le retour à l'héritage n'est proposé qu'au niveau d'un projet : l'instance
  // n'a personne dont hériter, et le bouton n'y voudrait rien dire.
  it("ne propose le retour au réglage de l'instance qu'à la portée projet", async () => {
    const onClear = vi.fn()
    const { unmount } = render(
      <PluginCard plugin={redmine} scope="global" saving={false} onSave={vi.fn()} />,
    )
    expect(screen.queryByRole('button', { name: /réglage de l'instance/ })).toBeNull()
    unmount()

    render(
      <PluginCard
        plugin={redmine}
        scope="project"
        saving={false}
        onSave={vi.fn()}
        onClear={onClear}
      />,
    )
    await userEvent.click(screen.getByRole('button', { name: /réglage de l'instance/ }))
    expect(onClear).toHaveBeenCalledTimes(1)
  })
})
