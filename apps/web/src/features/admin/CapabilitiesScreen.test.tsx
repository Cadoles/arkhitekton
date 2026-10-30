import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { render } from '../../test/render'
import { CapabilityCard, bindingOf, draftOf } from './CapabilitiesScreen'
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

describe('CapabilityCard', () => {
  it('montre en lecture seule ce que lie le fichier', () => {
    render(<CapabilityCard capability={{ ...gotest, bound_by: 'file' }} />)

    expect(screen.getByText(/se modifie avec le déploiement/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Enregistrer la liaison' })).not.toBeInTheDocument()
  })

  it("dit à quoi sert la ConfigMap qu'elle attend", () => {
    render(<CapabilityCard capability={audit} />)

    expect(screen.getByLabelText(/^ConfigMap de/)).toBeInTheDocument()
    expect(screen.getByText(/le barème de sévérité/)).toBeInTheDocument()
  })

  it('propose de lier ce que le fichier ne lie pas', async () => {
    render(<CapabilityCard capability={gotest} />)

    await userEvent.type(screen.getByLabelText(/Besoin/), 'git-credentials')
    expect(screen.getByRole('button', { name: 'Enregistrer la liaison' })).toBeEnabled()
    expect(screen.queryByRole('button', { name: 'Délier' })).not.toBeInTheDocument()
  })
})
