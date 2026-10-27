import type { ComponentType } from 'react'
import type { Block } from './interface'

/*
 * Les composants que les blocs d'un écran nomment.
 *
 * `core/…` sont ceux du noyau, enregistrés au démarrage ; `<plugin>/…` ceux
 * qu'un plugin installé enregistre depuis son script (`registerComponent`,
 * exporté par `@arkhitekton/host`). Le serveur a vérifié qu'un plugin ne nomme
 * que les siens et ceux du noyau.
 */

/** Ce qu'un composant de bloc reçoit : son bloc, ses `props` déclarées, et ce
 *  que l'emplacement qui le rend lui passe. */
export type BlockComponentProps = { block: Block } & Record<string, unknown>

const components = new Map<string, ComponentType<BlockComponentProps>>()

/** Enregistre un composant sous son nom. Un second enregistrement remplace le
 *  premier : un script chargé deux fois ne double rien. */
export function registerComponent(name: string, component: ComponentType<BlockComponentProps>): void {
  components.set(name, component)
}

/** Le composant d'un nom, s'il est enregistré. */
export function componentFor(name: string): ComponentType<BlockComponentProps> | undefined {
  return components.get(name)
}
