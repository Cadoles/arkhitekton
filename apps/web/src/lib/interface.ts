import { createContext, useContext } from 'react'
import { request } from './api'
import { currentProject } from './project'

/*
 * L'interface composée par le serveur : les écrans, menus et widgets du noyau
 * et des plugins allumés pour le projet courant, filtrés pour qui regarde
 * (`GET /ui`, doc 06 §6.14).
 *
 * Le navigateur ne fusionne rien : il construit son routeur et ses menus à
 * partir de ce qu'il reçoit. Afficher n'est pas autoriser : l'API reste seule
 * juge de ce qu'on peut faire.
 */

export type Condition = { field: string; equals?: unknown; in?: unknown[]; present?: boolean }

export type DataSource = {
  source: 'core' | 'plugin'
  name?: string
  params?: Record<string, string>
  path?: string
  /** Rempli par le serveur, depuis la route que le chemin désigne. */
  plugin?: string
  scope?: 'project' | 'instance'
}

export type Tone = string | { from: string; map?: Record<string, string>; default?: string }

export type Badge = { text: string; tone?: Tone }

export type FormField = {
  name: string
  label: string
  type?: 'text' | 'textarea' | 'boolean' | 'select'
  required?: boolean
  oneOf?: string[]
  placeholder?: string
  help?: string
}

export type Action = {
  label: string
  method: 'POST' | 'PUT' | 'DELETE'
  path: string
  confirm?: { text: string; challenge?: string }
  form?: FormField[]
  invalidates?: string[]
  danger?: boolean
  permission?: string
  plugin?: string
  scope?: 'project' | 'instance'
}

export type Column = {
  label: string
  value?: string
  link?: string
  mono?: boolean
  badge?: Badge
  action?: Action
  when?: Condition[]
}

/** Un widget : exactement un de ses champs, affiché sous condition. */
export type Widget = {
  when?: Condition[]
  stack?: { row?: boolean; children: Widget[] }
  grid?: { columns: number; children: Widget[] }
  card?: { title?: string; collapsible?: boolean; collapsed?: boolean; children: Widget[] }
  heading?: { text: string; level?: number }
  keyValue?: { items: { label: string; value: string; link?: string; mono?: boolean }[] }
  table?: { rows: string; empty?: string; columns: Column[] }
  list?: { items: string; empty?: string; item: Widget }
  badge?: Badge
  stat?: { label: string; value: string; hint?: string; tone?: Tone }
  text?: { value: string; muted?: boolean; mono?: boolean }
  markdown?: { value: string }
  link?: { label: string; to: string }
  action?: Action
  empty?: { text: string }
  use?: { widget: string; with?: Record<string, string> }
  tabs?: { navigation: string }
  outlet?: Record<string, never>
}

export type Block = {
  id: string
  component?: string
  props?: Record<string, unknown>
  widget?: Widget
  data?: Record<string, DataSource>
  when?: Condition[]
  permission?: string
  slots?: Record<string, Block[]>
}

export type Screen = {
  name: string
  path?: string
  parent?: string
  redirect?: string
  permission?: string
  data?: Record<string, DataSource>
  blocks?: Block[]
}

export type NavItem = {
  id: string
  label: string
  icon?: string
  to: string
  end?: boolean
  permission?: string
}

export type Navigation = { home?: string; items?: NavItem[] }

export type WidgetDefinition = { params?: Record<string, { required?: boolean }>; widget: Widget }

export type UIInterface = {
  screens: Screen[]
  navigations: Record<string, Navigation>
  widgets: Record<string, WidgetDefinition>
  eventLabels: Record<string, string>
}

/** L'interface vide : rien de chargé, ou rien à montrer. */
export const EMPTY_INTERFACE: UIInterface = { screens: [], navigations: {}, widgets: {}, eventLabels: {} }

/** Charge l'interface du projet courant. */
export function loadInterface(): Promise<UIInterface> {
  const project = currentProject()
  return request<UIInterface>(project ? `/ui?project=${encodeURIComponent(project)}` : '/ui')
}

let loaded: UIInterface = EMPTY_INTERFACE

/** Retient l'interface chargée, pour ce qui la lit hors de l'arbre React. */
export function setInterface(ui: UIInterface): void {
  loaded = ui
}

/** L'interface chargée. */
export function currentInterface(): UIInterface {
  return loaded
}

export const InterfaceContext = createContext<UIInterface>(EMPTY_INTERFACE)

export function useInterface(): UIInterface {
  return useContext(InterfaceContext)
}

/** Une navigation, vide si l'interface n'en a pas de ce nom. */
export function useNavigation(name: string): Navigation {
  return useInterface().navigations[name] ?? {}
}
