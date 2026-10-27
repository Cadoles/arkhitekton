import { Component, createContext, createElement, useContext, type ReactNode } from 'react'
import { ErrorState } from './primitives'
import { componentFor } from '../lib/components'
import type { Block, Screen } from '../lib/interface'

/*
 * Le rendu d'un écran décrit en données : ses blocs, dans l'ordre que le
 * serveur a composé.
 *
 * Chaque bloc a sa frontière d'erreur. Un bloc qui lève, ou qui nomme un
 * composant inconnu, se signale à sa place, et le reste de l'écran s'affiche :
 * un plugin en panne ne doit pas emporter l'écran du noyau qu'il complète.
 */

export function ScreenView({ screen }: { screen: Screen }) {
  const blocks = screen.blocks ?? []
  if (blocks.length === 1 && blocks[0]) return <BlockView block={blocks[0]} />
  return (
    <div className="flex flex-col gap-6">
      {blocks.map((block) => (
        <BlockView key={block.id} block={block} />
      ))}
    </div>
  )
}

const SlotsContext = createContext<Block['slots']>(undefined)

/** Un bloc, et ce que son composant reçoit d'un emplacement qui le rend. */
export function BlockView({ block, context = {} }: { block: Block; context?: Record<string, unknown> }) {
  return (
    <BlockBoundary id={block.id}>
      <SlotsContext.Provider value={block.slots}>
        <BlockContent block={block} context={context} />
      </SlotsContext.Provider>
    </BlockBoundary>
  )
}

function BlockContent({ block, context }: { block: Block; context: Record<string, unknown> }) {
  if (block.component) {
    const registered = componentFor(block.component)
    if (!registered) return <ErrorState message={`Composant inconnu : ${block.component}.`} />
    // Lu dans le registre, pas créé au rendu : le même nom rend toujours le même
    // composant, son état survit d'un rendu à l'autre.
    return createElement(registered, { ...context, ...block.props, block })
  }
  return null
}

/**
 * Les blocs qu'on a ajoutés dans un emplacement du composant courant.
 *
 * `context` est ce que le composant passe à chacun : la release d'une ligne,
 * par exemple. Un emplacement vide ne rend rien.
 */
export function Slot({ name, context = {} }: { name: string; context?: Record<string, unknown> }) {
  const entries = useContext(SlotsContext)?.[name] ?? []
  return (
    <>
      {entries.map((entry) => (
        <BlockView key={entry.id} block={entry} context={context} />
      ))}
    </>
  )
}

type BoundaryState = { failed: boolean }

class BlockBoundary extends Component<{ id: string; children: ReactNode }, BoundaryState> {
  state: BoundaryState = { failed: false }

  static getDerivedStateFromError(): BoundaryState {
    return { failed: true }
  }

  componentDidCatch(error: unknown) {
    console.error(`bloc ${this.props.id} :`, error)
  }

  render() {
    if (this.state.failed) {
      return <ErrorState message={`Le bloc « ${this.props.id} » n'a pas pu s'afficher.`} />
    }
    return this.props.children
  }
}
