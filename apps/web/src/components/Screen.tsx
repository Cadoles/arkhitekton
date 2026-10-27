import { Component, createContext, createElement, useContext, type ReactNode } from 'react'
import { useParams } from 'react-router'
import { ErrorState, Skeleton } from './primitives'
import { InvalidateContext, ScopeContext, WidgetView, useScope } from './Widget'
import { componentFor } from '../lib/components'
import { holds } from '../lib/conditions'
import { useData } from '../lib/data'
import type { Block, DataSource, Screen } from '../lib/interface'

/*
 * Le rendu d'un écran décrit en données : ses blocs, dans l'ordre que le
 * serveur a composé, chacun avec ses données et ses conditions.
 *
 * Chaque bloc a sa frontière d'erreur. Un bloc qui lève, ou qui nomme un
 * composant inconnu, se signale à sa place, et le reste de l'écran s'affiche :
 * un plugin en panne ne doit pas emporter l'écran du noyau qu'il complète.
 */

export function ScreenView({ screen }: { screen: Screen }) {
  const route = useParams()
  const blocks = screen.blocks ?? []
  return (
    <ScopeContext.Provider value={[{ route }]}>
      <WithData sources={screen.data}>
        {blocks.length === 1 && blocks[0] ? (
          <BlockView block={blocks[0]} />
        ) : (
          <div className="flex flex-col gap-5">
            {blocks.map((block) => (
              <BlockView key={block.id} block={block} />
            ))}
          </div>
        )}
      </WithData>
    </ScopeContext.Provider>
  )
}

/** Charge des sources et les ajoute à la portée des gabarits, ce qui suit
 *  attendant qu'elles soient là : une condition lue avant ferait sauter
 *  l'écran. */
function WithData({ sources, children }: { sources?: Record<string, DataSource>; children: ReactNode }) {
  const scope = useScope()
  const parentInvalidate = useContext(InvalidateContext)
  const data = useData(sources, scope)
  if (!sources || Object.keys(sources).length === 0) return <>{children}</>
  if (data.pending) return <Skeleton className="h-24 w-full" />
  if (data.error) return <ErrorState message="Les données de ce bloc n'ont pas pu être chargées." onRetry={data.retry} />
  return (
    <ScopeContext.Provider value={[data.values, ...scope]}>
      <InvalidateContext.Provider
        value={async (names) => {
          await Promise.all([data.invalidate(names), parentInvalidate(names)])
        }}
      >
        {children}
      </InvalidateContext.Provider>
    </ScopeContext.Provider>
  )
}

const SlotsContext = createContext<Block['slots']>(undefined)

/** Un bloc, et ce que son composant reçoit d'un emplacement qui le rend. */
export function BlockView({ block, context }: { block: Block; context?: Record<string, unknown> }) {
  const scope = useScope()
  const inner = context ? [context, ...scope] : scope
  return (
    <BlockBoundary id={block.id}>
      <ScopeContext.Provider value={inner}>
        <WithData sources={block.data}>
          <SlotsContext.Provider value={block.slots}>
            <BlockContent block={block} context={context ?? {}} />
          </SlotsContext.Provider>
        </WithData>
      </ScopeContext.Provider>
    </BlockBoundary>
  )
}

function BlockContent({ block, context }: { block: Block; context: Record<string, unknown> }) {
  const scope = useScope()
  if (!holds(block.when, scope)) return null
  if (block.component) {
    const registered = componentFor(block.component)
    if (!registered) return <ErrorState message={`Composant inconnu : ${block.component}.`} />
    // Lu dans le registre, pas créé au rendu : le même nom rend toujours le même
    // composant, son état survit d'un rendu à l'autre.
    return createElement(registered, { ...context, ...block.props, block })
  }
  if (block.widget) return <WidgetView widget={block.widget} />
  return null
}

/**
 * Les blocs qu'on a ajoutés dans un emplacement du composant courant.
 *
 * `context` est ce que le composant passe à chacun, et ce que leurs gabarits
 * lisent en premier : la release d'une ligne, par exemple. Un emplacement vide
 * ne rend rien.
 */
export function Slot({ name, context }: { name: string; context?: Record<string, unknown> }) {
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
