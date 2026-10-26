import * as React from 'react'
import * as jsxRuntime from 'react/jsx-runtime'
import * as ReactDOM from 'react-dom'
import * as ReactRouter from 'react-router'
import * as ReactQuery from '@tanstack/react-query'
import * as host from '../host'
import { SHARED } from './shared'

/*
 * L'interface des plugins installés, chargée sans reconstruire le noyau.
 *
 * Le script d'un plugin est construit à part, sans React ni routeur : il prend
 * ceux du noyau dans `window.__arkhitekton`, pour qu'il n'y ait qu'un React,
 * qu'un routeur et qu'un cache de requêtes. Les noms sous lesquels il les trouve
 * sont ceux du preset de build (`plugin.vite.ts`) : les deux bougent ensemble.
 *
 * Le script est servi par le dashboard, depuis l'origine de la page : la CSP
 * (`script-src 'self'`) n'a pas à changer. Il agit avec la session de
 * l'utilisateur, ce que l'administrateur accepte en installant le plugin.
 */

/** Une interface à charger, telle que `/me` la décrit. */
export type PluginInterface = {
  plugin: string
  script: string
  styles: string[]
}

declare global {
  interface Window {
    __arkhitekton?: Record<string, unknown>
  }
}

/** Expose au script des plugins ce qu'il ne doit pas embarquer. */
export function exposeShared(): void {
  const lent: Record<keyof typeof SHARED, unknown> = {
    react: React,
    'react/jsx-runtime': jsxRuntime,
    'react-dom': ReactDOM,
    'react-router': ReactRouter,
    '@tanstack/react-query': ReactQuery,
    '@arkhitekton/host': host,
  }
  window.__arkhitekton = Object.fromEntries(
    Object.entries(SHARED).map(([id, name]) => [name, lent[id as keyof typeof SHARED]]),
  )
}

/** Charge les interfaces avant le premier écran. Une interface qui échoue ou
 *  tarde est laissée de côté, et le dit dans la console : un plugin en panne ne
 *  doit pas empêcher d'ouvrir le dashboard. */
export async function loadInterfaces(
  interfaces: PluginInterface[],
  timeout = 10_000,
): Promise<void> {
  for (const entry of interfaces) {
    for (const href of entry.styles) {
      const link = document.createElement('link')
      link.rel = 'stylesheet'
      link.href = href
      document.head.appendChild(link)
    }
  }
  await Promise.all(interfaces.map((entry) => loadScript(entry, timeout)))
}

function loadScript(entry: PluginInterface, timeout: number): Promise<void> {
  return new Promise((resolve) => {
    const script = document.createElement('script')
    script.src = entry.script
    script.async = true
    const done = (problem?: string) => {
      window.clearTimeout(timer)
      if (problem) console.error(`interface du plugin ${entry.plugin} : ${problem}`)
      resolve()
    }
    const timer = window.setTimeout(() => done('pas chargée à temps'), timeout)
    script.addEventListener('load', () => done())
    script.addEventListener('error', () => done('échec du chargement'))
    document.head.appendChild(script)
  })
}
