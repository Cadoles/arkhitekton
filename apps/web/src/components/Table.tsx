import type { ReactNode } from 'react'

/*
 * Un tableau, dans la grammaire des écrans : en-têtes discrets, lignes séparées
 * d'un filet, défilement horizontal plutôt qu'une page qui déborde.
 */

export type TableColumn = { label: string; mono?: boolean }

export function Table({
  columns,
  rows,
  label,
}: {
  columns: TableColumn[]
  /** Les cellules de chaque ligne, dans l'ordre des colonnes. */
  rows: ReactNode[][]
  label?: string
}) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-left text-sm" aria-label={label}>
        <thead>
          <tr className="border-b border-zinc-200 text-xs font-medium text-zinc-500">
            {columns.map((column, index) => (
              <th key={index} scope="col" className="px-2 py-1.5 font-medium">
                {column.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((cells, rowIndex) => (
            <tr key={rowIndex} className="border-b border-zinc-100 last:border-0">
              {cells.map((cell, index) => (
                <td
                  key={index}
                  className={`px-2 py-1.5 align-middle text-zinc-900 ${columns[index]?.mono ? 'font-mono text-xs' : ''}`}
                >
                  {cell}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
