import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { render } from '../../test/render'
import { PluginRow, PluginSettings } from './PluginCard'
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
  label: 'Redmine',
  capabilities: [{ kind: 'tracker', kind_label: 'Suivi', name: 'tracker', label: 'Suivi' }],
  summary: 'Versions et demandes.',
  per_project: true,
  mandatory: false,
  fields: [
    { name: 'url', label: "URL de l'instance", type: 'url' },
    { name: 'api_key', label: "Clé d'API", type: 'text', secret: true },
  ],
  enabled: true,
  config: { url: 'https://redmine.example.fr' },
  secrets_set: ['api_key'],
  secrets_configured: true,
}

describe('les réglages d’un plugin', () => {
  // La valeur d'un champ sensible n'est jamais rendue par l'API, donc jamais
  // affichée. L'écran n'en connaît que la présence — c'est ce qui rend la saisie
  // d'une clé depuis un navigateur acceptable.
  it("n'affiche jamais la valeur d'un champ sensible, seulement sa présence", () => {
    render(<PluginSettings plugin={redmine} scope="global" saving={false} onSave={vi.fn()} />)

    const cle = screen.getByLabelText("Clé d'API")
    expect(cle).toHaveValue('')
    expect(cle).toHaveAttribute('placeholder', expect.stringContaining('enregistrée'))
  })

  // Le piège que ce test existe pour éviter : l'écran ne reçoit jamais la clé,
  // donc ne peut pas la renvoyer. La soumettre vide l'effacerait — et modifier
  // une URL suffirait alors à couper la synchronisation.
  it('soumet un champ sensible vide sans demander son effacement', async () => {
    const onSave = vi.fn()
    render(<PluginSettings plugin={redmine} scope="global" saving={false} onSave={onSave} />)

    await userEvent.click(screen.getByRole('button', { name: 'Enregistrer' }))

    expect(onSave).toHaveBeenCalledTimes(1)
    const draft = onSave.mock.calls[0]![0]
    expect(draft.config.api_key ?? '').toBe('')
    expect(draft.cleared_secrets).toBeUndefined()
  })

  // L'effacement existe, et demande un geste distinct de « laisser vide ».
  it("demande explicitement l'effacement d'un champ sensible", async () => {
    const onSave = vi.fn()
    render(<PluginSettings plugin={redmine} scope="global" saving={false} onSave={onSave} />)

    await userEvent.click(screen.getByRole('button', { name: 'Effacer' }))
    await userEvent.click(screen.getByRole('button', { name: 'Enregistrer' }))

    expect(onSave.mock.calls[0]![0].cleared_secrets).toEqual(['api_key'])
  })

  // Sans scellement configuré, le champ est désactivé plutôt que laissé saisir :
  // le serveur refuserait l'enregistrement, et découvrir le refus après avoir
  // tapé une clé de production est le pire moment pour l'apprendre.
  it('désactive les champs sensibles quand le scellement manque', () => {
    render(
      <PluginSettings
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
      <PluginSettings plugin={redmine} scope="global" saving={false} onSave={vi.fn()} />,
    )
    expect(screen.queryByRole('button', { name: /réglage de l'instance/ })).toBeNull()
    unmount()

    render(
      <PluginSettings
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

  // L'adresse se colle telle quelle dans la forge : complète, avec l'origine.
  it("montre l'adresse du webhook d'un plugin qui en reçoit", () => {
    render(
      <PluginSettings
        plugin={{ ...redmine, webhook_path: '/api/v1/hooks/gitea/projects/mse' }}
        scope="project"
        saving={false}
        onSave={vi.fn()}
      />,
    )

    expect(screen.getByLabelText('Adresse du webhook')).toHaveValue(
      `${window.location.origin}/api/v1/hooks/gitea/projects/mse`,
    )
  })

  it("ne montre aucune adresse pour un plugin qui n'en reçoit pas", () => {
    render(<PluginSettings plugin={redmine} scope="project" saving={false} onSave={vi.fn()} />)
    expect(screen.queryByLabelText('Adresse du webhook')).not.toBeInTheDocument()
  })
})

describe('la ligne d’un plugin', () => {
  // Un plugin sans lequel le dashboard ne fonctionne pas n'offre pas d'interrupteur :
  // le serveur refuserait de l'éteindre.
  it("n'offre pas d'interrupteur pour un plugin obligatoire", () => {
    render(
      <PluginRow
        plugin={{ ...redmine, id: 'core', label: 'Socle', mandatory: true, fields: [] }}
        scope="global"
        saving={false}
        onSave={vi.fn()}
      />,
    )

    expect(screen.queryByRole('switch')).toBeNull()
    expect(screen.getByText('Toujours actif')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Paramètres de Socle' })).toBeDisabled()
  })

  // L'interrupteur n'enregistre que l'activation : le réglage reste celui qui est
  // enregistré, la clé d'API comprise, qui n'est pas renvoyée.
  it("bascule l'activation sans toucher au réglage", async () => {
    const onSave = vi.fn()
    render(<PluginRow plugin={redmine} scope="global" saving={false} onSave={onSave} />)

    const toggle = screen.getByRole('switch', { name: "Activer Redmine sur l'instance" })
    expect(toggle).toHaveAttribute('aria-checked', 'true')
    await userEvent.click(toggle)

    expect(onSave).toHaveBeenCalledWith({ enabled: false, config: redmine.config })
  })

  it('ouvre ses réglages dans une fenêtre, qui garde l’activation', async () => {
    const onSave = vi.fn()
    render(<PluginRow plugin={{ ...redmine, enabled: false }} scope="global" saving={false} onSave={onSave} />)

    await userEvent.click(screen.getByRole('button', { name: 'Paramètres de Redmine' }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Enregistrer' }))

    expect(onSave.mock.calls[0]![0].enabled).toBe(false)
  })

  // Activé mais sans ce qu'il lui faut, un plugin ne fait rien : la ligne le dit.
  it('signale un plugin activé à qui manque un champ requis', () => {
    const required = {
      ...redmine,
      fields: [{ name: 'url', label: "URL de l'instance", type: 'url' as const, required: true }],
      config: {},
    }
    render(<PluginRow plugin={required} scope="global" saving={false} onSave={vi.fn()} />)

    expect(screen.getByText('à configurer')).toBeInTheDocument()
  })
})
