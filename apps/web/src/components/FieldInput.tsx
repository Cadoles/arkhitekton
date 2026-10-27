import { useId } from 'react'

/*
 * Un champ de formulaire : texte, texte long, case à cocher ou choix fermé. La
 * valeur est toujours une chaîne, `true`/`false` pour une case : c'est ainsi
 * que réglages et actions la transportent.
 */

export type FieldSpec = {
  label: string
  type?: 'text' | 'textarea' | 'boolean' | 'select'
  required?: boolean
  oneOf?: string[]
  placeholder?: string
  help?: string
}

const CONTROL = 'w-full rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm text-zinc-900'

export function FieldInput({
  field,
  value,
  onChange,
}: {
  field: FieldSpec
  value: string
  onChange: (value: string) => void
}) {
  const id = useId()

  if (field.type === 'boolean') {
    return (
      <label htmlFor={id} className="flex items-center gap-2 text-sm text-zinc-700">
        <input
          id={id}
          type="checkbox"
          checked={value === 'true'}
          onChange={(event) => onChange(event.target.checked ? 'true' : 'false')}
          className="size-4 rounded border-zinc-300"
        />
        {field.label}
      </label>
    )
  }

  return (
    <div className="flex flex-col gap-1">
      <label htmlFor={id} className="text-sm font-medium text-zinc-700">
        {field.label}
        {field.required && <span className="ml-1 text-critical-700">*</span>}
      </label>
      {field.type === 'select' ? (
        <select id={id} value={value} onChange={(event) => onChange(event.target.value)} className={CONTROL}>
          <option value="">(aucun)</option>
          {(field.oneOf ?? []).map((choice) => (
            <option key={choice} value={choice}>
              {choice}
            </option>
          ))}
        </select>
      ) : field.type === 'textarea' ? (
        <textarea
          id={id}
          value={value}
          rows={4}
          placeholder={field.placeholder}
          onChange={(event) => onChange(event.target.value)}
          className={CONTROL}
        />
      ) : (
        <input
          id={id}
          type="text"
          value={value}
          placeholder={field.placeholder}
          onChange={(event) => onChange(event.target.value)}
          className={CONTROL}
        />
      )}
      {field.help && <p className="text-xs text-zinc-500">{field.help}</p>}
    </div>
  )
}
