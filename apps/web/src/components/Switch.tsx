/**
 * Un interrupteur : un état, basculé d'un geste.
 *
 * Un bouton au rôle `switch`, et non une case à cocher : il dit l'état présent
 * et le change aussitôt, sans formulaire à valider.
 */
export function Switch({
  checked,
  label,
  disabled = false,
  onChange,
}: {
  checked: boolean
  /** Ce que l'interrupteur active, pour qui ne le voit pas. */
  label: string
  disabled?: boolean
  onChange: (checked: boolean) => void
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={`relative inline-flex h-5 w-9 shrink-0 items-center rounded-full border transition-colors disabled:opacity-50 ${
        checked ? 'border-healthy-500 bg-healthy-500' : 'border-zinc-300 bg-zinc-200'
      }`}
    >
      <span
        className={`absolute left-0.5 size-3.5 rounded-full bg-white shadow-sm transition-transform ${
          checked ? 'translate-x-4' : 'translate-x-0'
        }`}
      />
    </button>
  )
}
