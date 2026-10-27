import { createContext, useContext, useState, type ReactNode } from 'react'
import { useMutation } from '@tanstack/react-query'
import { Outlet } from 'react-router'
import { ApiError, request } from '../lib/api'
import { holds } from '../lib/conditions'
import { pluginPath } from '../lib/data'
import { useInterface, type Action, type Badge, type Tone, type Widget } from '../lib/interface'
import { formatValue, lookup, renderTemplate, type Scope } from '../lib/template'
import { CollapsibleSection } from './CollapsibleSection'
import { ConfirmDialog } from './ConfirmDialog'
import { FieldInput } from './FieldInput'
import { Markdown, TextLink } from './Markdown'
import { Modal } from './Modal'
import { NavTabs } from './NavTabs'
import { Card, EmptyState } from './primitives'
import { Table } from './Table'

/*
 * Les widgets déclaratifs : un vocabulaire fermé, que seul le noyau étend
 * (`sdk/ui`). Chacun lit les données de son bloc par des gabarits ; dans un
 * tableau ou une liste, les champs de la ligne d'abord.
 */

/** Les données que les gabarits lisent, de la plus proche à la plus lointaine. */
export const ScopeContext = createContext<Scope>([])

/** Relit les données nommées après une action. */
export const InvalidateContext = createContext<(names?: string[]) => Promise<void>>(async () => {})

export function useScope(): Scope {
  return useContext(ScopeContext)
}

const TONES: Record<string, string> = {
  neutral: 'border-zinc-300 bg-zinc-100 text-zinc-700',
  info: 'border-zinc-400 bg-white text-zinc-700',
  success: 'border-healthy-500 bg-healthy-50 text-healthy-700',
  warning: 'border-warn-500 bg-warn-50 text-warn-700',
  danger: 'border-critical-500 bg-critical-50 text-critical-700',
}

const TONE_TEXT: Record<string, string> = {
  neutral: 'text-zinc-900',
  info: 'text-zinc-900',
  success: 'text-healthy-700',
  warning: 'text-warn-700',
  danger: 'text-critical-700',
}

function toneOf(tone: Tone | undefined, scope: Scope): string {
  if (!tone) return 'neutral'
  if (typeof tone === 'string') return tone
  return tone.map?.[formatValue(lookup(scope, tone.from))] ?? tone.default ?? 'neutral'
}

function BadgeView({ badge, scope }: { badge: Badge; scope: Scope }) {
  const tone = toneOf(badge.tone, scope)
  return (
    <span
      className={`inline-flex items-center rounded-md border px-2 py-0.5 text-xs font-medium ${TONES[tone] ?? TONES.neutral}`}
    >
      {renderTemplate(badge.text, scope)}
    </span>
  )
}

function Linked({ to, children }: { to: string; children: ReactNode }) {
  return to ? <TextLink to={to}>{children}</TextLink> : <>{children}</>
}

export function WidgetView({ widget }: { widget: Widget }) {
  const scope = useScope()
  if (!holds(widget.when, scope)) return null
  return <WidgetContent widget={widget} scope={scope} />
}

function Children({ widgets }: { widgets: Widget[] }) {
  return (
    <>
      {widgets.map((child, index) => (
        <WidgetView key={index} widget={child} />
      ))}
    </>
  )
}

function rowsOf(scope: Scope, path: string): unknown[] {
  const value = lookup(scope, path)
  return Array.isArray(value) ? value : []
}

function within(row: unknown, scope: Scope): Scope {
  return [row !== null && typeof row === 'object' ? (row as Record<string, unknown>) : { value: row }, ...scope]
}

function WidgetContent({ widget, scope }: { widget: Widget; scope: Scope }) {
  const ui = useInterface()

  if (widget.stack) {
    return (
      <div className={widget.stack.row ? 'flex flex-wrap items-center gap-2' : 'flex flex-col gap-3'}>
        <Children widgets={widget.stack.children} />
      </div>
    )
  }
  if (widget.grid) {
    const columns = ['', 'sm:grid-cols-1', 'sm:grid-cols-2', 'sm:grid-cols-3', 'sm:grid-cols-4'][widget.grid.columns] ?? ''
    return (
      <div className={`grid gap-4 ${columns}`}>
        <Children widgets={widget.grid.children} />
      </div>
    )
  }
  if (widget.card) {
    const title = renderTemplate(widget.card.title, scope)
    const content = (
      <div className="flex flex-col gap-3">
        <Children widgets={widget.card.children} />
      </div>
    )
    if (widget.card.collapsible) {
      return (
        <CollapsibleSection title={title} defaultOpen={!widget.card.collapsed}>
          {content}
        </CollapsibleSection>
      )
    }
    return <Card title={title || undefined}>{content}</Card>
  }
  if (widget.heading) {
    const text = renderTemplate(widget.heading.text, scope)
    if (widget.heading.level === 3) return <h3 className="text-sm font-semibold text-zinc-900">{text}</h3>
    if (widget.heading.level === 2) return <h2 className="text-lg font-semibold text-zinc-900">{text}</h2>
    return <h1 className="text-2xl font-semibold text-zinc-900">{text}</h1>
  }
  if (widget.keyValue) {
    return (
      <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-sm">
        {widget.keyValue.items.map((item, index) => (
          <div key={index} className="contents">
            <dt className="text-zinc-500">{item.label}</dt>
            <dd className={`text-zinc-900 ${item.mono ? 'font-mono text-xs' : ''}`}>
              <Linked to={renderTemplate(item.link, scope)}>{renderTemplate(item.value, scope)}</Linked>
            </dd>
          </div>
        ))}
      </dl>
    )
  }
  if (widget.table) {
    const table = widget.table
    const rows = rowsOf(scope, table.rows)
    if (rows.length === 0) return <EmptyState>{table.empty ?? 'Rien à afficher.'}</EmptyState>
    return (
      <Table
        columns={table.columns}
        rows={rows.map((row) => {
          const rowScope = within(row, scope)
          return table.columns.map((column) => {
            if (!holds(column.when, rowScope)) return null
            if (column.badge) return <BadgeView badge={column.badge} scope={rowScope} />
            if (column.action) return <ActionButton action={column.action} scope={rowScope} />
            return <Linked to={renderTemplate(column.link, rowScope)}>{renderTemplate(column.value, rowScope)}</Linked>
          })
        })}
      />
    )
  }
  if (widget.list) {
    const list = widget.list
    const items = rowsOf(scope, list.items)
    if (items.length === 0) return <EmptyState>{list.empty ?? 'Rien à afficher.'}</EmptyState>
    return (
      <div className="flex flex-col gap-2">
        {items.map((item, index) => (
          <ScopeContext.Provider key={index} value={within(item, scope)}>
            <WidgetView widget={list.item} />
          </ScopeContext.Provider>
        ))}
      </div>
    )
  }
  if (widget.badge) return <BadgeView badge={widget.badge} scope={scope} />
  if (widget.stat) {
    const tone = toneOf(widget.stat.tone, scope)
    return (
      <div className="flex flex-col">
        <span className="text-xs text-zinc-500">{widget.stat.label}</span>
        <span className={`text-2xl font-semibold ${TONE_TEXT[tone] ?? TONE_TEXT.neutral}`}>
          {renderTemplate(widget.stat.value, scope)}
        </span>
        {widget.stat.hint && <span className="text-xs text-zinc-500">{renderTemplate(widget.stat.hint, scope)}</span>}
      </div>
    )
  }
  if (widget.text) {
    return (
      <p
        className={`text-sm ${widget.text.muted ? 'text-zinc-500' : 'text-zinc-700'} ${widget.text.mono ? 'font-mono text-xs' : ''}`}
      >
        {renderTemplate(widget.text.value, scope)}
      </p>
    )
  }
  if (widget.markdown) return <Markdown text={renderTemplate(widget.markdown.value, scope)} />
  if (widget.link) {
    return (
      <span className="text-sm">
        <TextLink to={renderTemplate(widget.link.to, scope)}>{renderTemplate(widget.link.label, scope)}</TextLink>
      </span>
    )
  }
  if (widget.action) return <ActionButton action={widget.action} scope={scope} />
  if (widget.empty) return <EmptyState>{widget.empty.text}</EmptyState>
  if (widget.use) {
    const definition = ui.widgets[widget.use.widget]
    if (!definition) return null
    const bound = Object.fromEntries(
      Object.entries(widget.use.with ?? {}).map(([name, path]) => [name, lookup(scope, path)]),
    )
    return (
      <ScopeContext.Provider value={[bound, ...scope]}>
        <WidgetView widget={definition.widget} />
      </ScopeContext.Provider>
    )
  }
  if (widget.tabs) return <NavTabs navigation={widget.tabs.navigation} label={widget.tabs.navigation} />
  if (widget.outlet) return <Outlet />
  return null
}

const BUTTON = 'rounded-md border px-3 py-1 text-sm font-medium transition-colors disabled:opacity-50'
const BUTTON_TONE = {
  plain: 'border-zinc-300 bg-white text-zinc-700 hover:bg-zinc-100',
  danger: 'border-critical-600 bg-critical-50 text-critical-700 hover:border-critical-700',
}

/** Un bouton qui appelle une route du plugin, après confirmation et
 *  formulaire s'il en demande, puis relit les données qu'il change. */
export function ActionButton({ action, scope }: { action: Action; scope: Scope }) {
  const invalidate = useContext(InvalidateContext)
  const [asking, setAsking] = useState(false)
  const [values, setValues] = useState<Record<string, string>>({})
  const [typed, setTyped] = useState('')
  const mutation = useMutation({
    mutationFn: () =>
      request<unknown>(pluginPath(action.plugin ?? '', action.scope, renderTemplate(action.path, scope)), {
        method: action.method,
        body: action.form ? JSON.stringify(values) : undefined,
      }),
    onSuccess: async () => {
      setAsking(false)
      await invalidate(action.invalidates)
    },
  })
  const run = () => mutation.mutate()
  const confirmText = action.confirm ? renderTemplate(action.confirm.text, scope) : ''
  const challenge = action.confirm?.challenge ? renderTemplate(action.confirm.challenge, scope) : undefined
  const failure =
    mutation.error instanceof ApiError
      ? (mutation.error.detail ?? mutation.error.message)
      : mutation.error
        ? String(mutation.error)
        : ''

  return (
    <span className="inline-flex flex-col items-start gap-1">
      <button
        type="button"
        disabled={mutation.isPending}
        onClick={() => (action.confirm || action.form ? setAsking(true) : run())}
        className={`${BUTTON} ${action.danger ? BUTTON_TONE.danger : BUTTON_TONE.plain}`}
      >
        {action.label}
      </button>
      {failure && !asking && <span className="text-xs text-critical-700">{failure}</span>}
      {asking && !action.form && (
        <ConfirmDialog
          title={action.label}
          confirmLabel={action.label}
          challenge={challenge}
          pending={mutation.isPending}
          onConfirm={run}
          onCancel={() => setAsking(false)}
        >
          <p>{confirmText}</p>
          {failure && <p className="text-critical-700">{failure}</p>}
        </ConfirmDialog>
      )}
      {asking && action.form && (
        <Modal title={action.label} onClose={() => setAsking(false)}>
          <form
            className="flex flex-col gap-3"
            onSubmit={(event) => {
              event.preventDefault()
              run()
            }}
          >
            {confirmText && <p className="text-sm text-zinc-700">{confirmText}</p>}
            {action.form.map((field) => (
              <FieldInput
                key={field.name}
                field={field}
                value={values[field.name] ?? ''}
                onChange={(value) => setValues((previous) => ({ ...previous, [field.name]: value }))}
              />
            ))}
            {challenge && (
              <FieldInput
                field={{ label: `Saisir ${challenge} pour confirmer` }}
                value={typed}
                onChange={setTyped}
              />
            )}
            {failure && <p className="text-sm text-critical-700">{failure}</p>}
            <div className="flex justify-end gap-2">
              <button type="button" onClick={() => setAsking(false)} className={`${BUTTON} ${BUTTON_TONE.plain}`}>
                Annuler
              </button>
              <button
                type="submit"
                disabled={
                  mutation.isPending ||
                  (challenge !== undefined && typed.trim() !== challenge) ||
                  action.form.some((field) => field.required && !values[field.name])
                }
                className={`${BUTTON} ${action.danger ? BUTTON_TONE.danger : BUTTON_TONE.plain}`}
              >
                {action.label}
              </button>
            </div>
          </form>
        </Modal>
      )}
    </span>
  )
}
