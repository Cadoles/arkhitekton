import { Icon } from '../../components/Icon'

/** Pourquoi un job a échoué, sous sa ligne : un job refusé avant tout run n'a pas de journal
 *  où le lire. */
export function JobError({ error }: { error: string }) {
  return (
    <p className="flex w-full items-start gap-1 text-xs text-critical-700">
      <Icon name="alert" className="mt-0.5 size-3.5 shrink-0" />
      <span className="break-words">{error}</span>
    </p>
  )
}
