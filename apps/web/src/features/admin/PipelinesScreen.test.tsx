import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mockFetch } from '../../test/http'
import { render } from '../../test/render'
import { BindingForm, PipelineSettings, PipelinesScreen, bindingOf, draftOf } from './PipelinesScreen'
import type { Capability } from './capabilities'

/*
 * L'éditeur de liaisons.
 *
 * Il désigne des secrets et des ConfigMaps, et dimensionne des volumes : les tests
 * portent sur ce qu'il envoie (rien de vide, jamais de compte de service) et sur ce
 * qu'il ne laisse pas modifier (ce que lie le fichier).
 */

const gotest: Capability = {
  ref: 'gotest/unit-tests',
  label: 'Tests Go',
  plugin: 'gotest',
  needs: [{ name: 'source-read', reason: 'clone the project' }],
  params: ['repo-url'],
  volumes: ['shared-data'],
  config_maps: [],
  default_weight: 25,
  group: 'unit',
}

/** Une capacité qui attend une politique de l'exploitation. */
const audit: Capability = {
  ...gotest,
  ref: 'gotest/audit',
  config_maps: [{ workspace: 'policies', reason: 'le barème de sévérité' }],
}

describe('bindingOf', () => {
  it("n'envoie que ce qui est saisi", () => {
    const draft = draftOf(gotest)
    draft.needs['source-read'] = {
      source: 'configKey',
      value: ' pipeline.gitSecret ',
    }
    draft.volumes['shared-data'] = { storage: '2Gi', storageClassName: '' }

    expect(bindingOf(gotest.ref, draft)).toEqual({
      ref: 'gotest/unit-tests',
      needs: { 'source-read': { secret: { configKey: 'pipeline.gitSecret' } } },
      volumes: { 'shared-data': { storage: '2Gi' } },
    })
  })

  // Un compte de service est une identité de cluster : l'accorder est un acte de
  // déploiement, et l'API le refuserait de toute façon.
  it('ne lie jamais cluster-deploy', () => {
    const deploying = {
      ...gotest,
      needs: [...gotest.needs, { name: 'cluster-deploy' }],
    }
    const draft = draftOf(deploying)
    draft.needs['cluster-deploy'] = { source: 'secretName', value: 'admin' }

    expect(bindingOf(deploying.ref, draft).needs).toBeUndefined()
  })

  it('lie une ConfigMap par son nom ou par une clé', () => {
    const draft = draftOf(audit)
    draft.configMaps['policies'] = { source: 'configKey', value: ' security.policies ' }

    expect(bindingOf(audit.ref, draft).configMaps).toEqual({
      policies: { configKey: 'security.policies' },
    })
  })

  it("n'envoie pas une ConfigMap laissée vide", () => {
    expect(bindingOf(audit.ref, draftOf(audit)).configMaps).toBeUndefined()
  })

  it('relit une liaison enregistrée', () => {
    const bound: Capability = {
      ...gotest,
      bound_by: 'database',
      binding: {
        ref: gotest.ref,
        timeout: '20m',
        needs: { 'source-read': { secret: { secretName: 'git' } } },
      },
    }
    const draft = draftOf(bound)

    expect(draft.timeout).toBe('20m')
    expect(draft.needs['source-read']).toEqual({
      source: 'secretName',
      value: 'git',
    })
    expect(bindingOf(bound.ref, draft)).toEqual(bound.binding)
  })
})

describe('BindingForm', () => {
  it('montre en lecture seule ce que lie le fichier', () => {
    render(<BindingForm capability={{ ...gotest, bound_by: 'file' }} />)

    expect(screen.getByText(/se modifie avec le déploiement/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Enregistrer la liaison' })).not.toBeInTheDocument()
  })

  it("dit à quoi sert la ConfigMap qu'elle attend", () => {
    render(<BindingForm capability={audit} />)

    expect(screen.getByLabelText(/^ConfigMap de/)).toBeInTheDocument()
    expect(screen.getByText(/le barème de sévérité/)).toBeInTheDocument()
  })

  it('propose de lier ce que le fichier ne lie pas', async () => {
    render(<BindingForm capability={gotest} />)

    await userEvent.type(screen.getByLabelText(/Besoin/), 'git-credentials')
    expect(screen.getByRole('button', { name: 'Enregistrer la liaison' })).toBeEnabled()
    expect(screen.queryByRole('button', { name: 'Délier' })).not.toBeInTheDocument()
    // Le parallélisme se règle par la file, plus dans la liaison.
    expect(screen.queryByLabelText(/simultanés/)).not.toBeInTheDocument()
    expect(screen.getByLabelText(/Source du secret/).className).toContain('bg-white')
  })
})

describe('le poids dans le score', () => {
  beforeEach(() => vi.unstubAllGlobals())

  it("se règle même sur ce que lie le fichier, et dit ce qu'il vaut vide", async () => {
    const calls = mockFetch([
      { path: '/admin/capabilities/gotest/unit-tests/weight', method: 'PUT', body: { weight: 0 } },
    ])
    render(<PipelineSettings capability={{ ...gotest, bound_by: 'file' }} />)

    const field = screen.getByLabelText('Poids dans le score')
    expect(field).toHaveAttribute('placeholder', '25 (sa nature : unit)')
    await userEvent.type(field, '0')
    await userEvent.click(screen.getByRole('button', { name: 'Enregistrer' }))

    const sent = calls.find((call) => call.method === 'PUT')
    expect(sent?.body).toEqual({ weight: 0 })
  })

  it('dit hors score pour une nature que le score ne connaît pas', () => {
    render(<PipelineSettings capability={{ ...gotest, default_weight: 0, group: 'accessibility' }} />)

    expect(screen.getByLabelText('Poids dans le score')).toHaveAttribute('placeholder', 'hors score')
  })
})

const unitQueue = {
  kind: 'gotest/unit-tests', label: 'Tests Go', max_concurrent: 2, default_max_concurrent: 4,
  overridden: true, known: true, running: 0, waiting: 0, jobs: [],
}

describe("l'onglet Pipelines", () => {
  beforeEach(() => vi.unstubAllGlobals())

  it('tient un pipeline sur une ligne : liaison, score, parallélisme', async () => {
    mockFetch([
      { path: '/admin/capabilities', body: { capabilities: [{ ...gotest, bound_by: 'file' }, { ...audit, weight: 0, unavailable: 'no ConfigMap' }] } },
      { path: '/queue', body: { queues: [unitQueue], kinds: [], suggestions: {}, max_concurrent: 10 } },
    ])
    render(<PipelinesScreen />)

    const row = (await screen.findByText('gotest/unit-tests')).closest('tr') as HTMLElement
    expect(row).toHaveTextContent('Fichier')
    expect(row).toHaveTextContent('25 (nature)')
    expect(row).toHaveTextContent('2 (défaut 4)')
    const other = screen.getByText('gotest/audit').closest('tr') as HTMLElement
    expect(other).toHaveTextContent('Non liée')
    expect(other).toHaveTextContent('hors score')
    expect(screen.getByText(/Au plus 10 exécutions à la fois/)).toBeInTheDocument()
  })

  it('règle le parallélisme depuis la fenêtre des paramètres', async () => {
    const calls = mockFetch([
      { path: '/admin/capabilities', body: { capabilities: [gotest] } },
      { path: '/queue/limits/gotest%2Funit-tests', method: 'PUT', body: { kind: 'gotest/unit-tests' } },
      { path: '/queue/limits/gotest%2Funit-tests', method: 'DELETE', body: { kind: 'gotest/unit-tests' } },
      { path: '/queue', body: { queues: [unitQueue], kinds: [], suggestions: {}, max_concurrent: 10 } },
    ])
    render(<PipelinesScreen />)

    await userEvent.click(await screen.findByRole('button', { name: 'Paramètres de Tests Go' }))
    const field = screen.getByLabelText('Maximum en parallèle')
    await userEvent.clear(field)
    await userEvent.type(field, '3')
    await userEvent.click(screen.getAllByRole('button', { name: 'Enregistrer' })[1] as HTMLElement)
    expect(calls.find((call) => call.method === 'PUT')?.body).toEqual({ max_concurrent: 3 })

    await userEvent.click(screen.getByRole('button', { name: 'Revenir au défaut (4)' }))
    expect(calls.some((call) => call.method === 'DELETE')).toBe(true)
  })
})
