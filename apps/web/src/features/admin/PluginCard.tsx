import { useState } from 'react'
import { Icon } from '../../components/Icon'
import type { Plugin, PluginDraft, PluginField } from './plugins'
import { FieldInput } from '../../components/FieldInput'

/*
 * Le formulaire d'un plugin, à l'une ou l'autre portée.
 *
 * Le **même** composant pour l'instance et pour un projet, et ce n'est pas une
 * économie de lignes : les deux portées décrivent leurs champs par le même
 * contrat, et deux formulaires distincts finiraient par en interpréter un
 * différemment — celui du projet oubliant, par exemple, qu'un champ vide ne
 * s'efface pas.
 *
 * # Ce que l'écran ne montre jamais
 *
 * La valeur d'un champ sensible. Elle est scellée en base et n'en ressort que
 * pour l'appel sortant qui l'emploie. L'écran n'en connaît que la présence, et
 * c'est ce qui rend la saisie d'une clé d'API depuis un navigateur acceptable :
 * la relire ne suffit pas à la voler.
 */

export function PluginCard({
  plugin,
  scope,
  saving,
  onSave,
  onClear,
}: {
  plugin: Plugin
  scope: 'global' | 'project'
  saving: boolean
  onSave: (draft: PluginDraft) => void
  /** Présent à la portée projet seulement : rend le projet à l'héritage. */
  onClear?: () => void
}) {
  const [enabled, setEnabled] = useState(plugin.enabled)
  const [config, setConfig] = useState<Record<string, string>>(plugin.config)
  const [cleared, setCleared] = useState<string[]>([])

  const secretsSet = plugin.secrets_set.filter((name) => !cleared.includes(name))

  return (
    <li className="flex flex-col gap-3 rounded-md border border-zinc-200 px-3 py-3">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <span className="font-medium text-zinc-900">{plugin.label}</span>
        {[...new Set(plugin.capabilities.map((capability) => capability.kind_label))].map(
          (label) => (
            <span
              key={label}
              className="rounded border border-zinc-200 px-1.5 py-0.5 text-xs text-zinc-500"
            >
              {label}
            </span>
          ),
        )}

        {plugin.mandatory ? (
          <span className="ml-auto text-sm text-zinc-500">Toujours actif</span>
        ) : (
          <label className="ml-auto flex items-center gap-1.5 text-sm text-zinc-700">
            <input
              type="checkbox"
              checked={enabled}
              onChange={(event) => setEnabled(event.target.checked)}
              className="size-4 rounded border-zinc-300"
            />
            {scope === 'global' ? 'Disponible sur cette instance' : 'Appliqué à ce projet'}
          </label>
        )}
      </div>

      <p className="text-sm text-zinc-500">{plugin.summary}</p>

      {plugin.fields.length > 0 && (
        <div className="flex flex-col gap-3 border-t border-zinc-200 pt-3">
          {plugin.fields.map((field) => (
            <PluginFieldRow
              key={field.name}
              field={field}
              scope={scope}
              value={config[field.name] ?? ''}
              stored={secretsSet.includes(field.name)}
              sealingConfigured={plugin.secrets_configured}
              onChange={(value) => setConfig((current) => ({ ...current, [field.name]: value }))}
              onClearSecret={() => setCleared((current) => [...current, field.name])}
            />
          ))}
        </div>
      )}

      {!plugin.secrets_configured && plugin.fields.some((field) => field.secret) && (
        <p className="flex items-start gap-1.5 text-xs text-warn-700">
          <Icon name="alert" className="mt-0.5 size-3.5 shrink-0" />
          <span>
            Le scellement n'est pas configuré : renseignez{' '}
            <span className="font-mono">SECRETS_KEY</span> dans le déploiement
            (<span className="font-mono">openssl rand -base64 32</span>) pour pouvoir
            enregistrer les champs sensibles. Le dashboard refuse de les écrire en clair.
          </span>
        </p>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          disabled={saving}
          onClick={() =>
            onSave({
              enabled,
              config,
              cleared_secrets: cleared.length > 0 ? cleared : undefined,
            })
          }
          className="rounded-md border border-zinc-900 bg-zinc-900 px-2.5 py-1 text-xs font-medium text-white transition-colors hover:bg-zinc-700 disabled:opacity-40"
        >
          Enregistrer
        </button>

        {onClear && (
          <button
            type="button"
            onClick={onClear}
            title="Le projet suivra à nouveau le réglage de l'instance"
            className="rounded-md border border-zinc-300 bg-white px-2.5 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
          >
            Revenir au réglage de l'instance
          </button>
        )}

        {plugin.updated_by && (
          <span className="text-xs text-zinc-400">réglé par {plugin.updated_by}</span>
        )}
      </div>
    </li>
  )
}

function PluginFieldRow({
  field,
  scope,
  value,
  stored,
  sealingConfigured,
  onChange,
  onClearSecret,
}: {
  field: PluginField
  scope: 'global' | 'project'
  value: string
  stored: boolean
  sealingConfigured: boolean
  onChange: (value: string) => void
  onClearSecret: () => void
}) {
  const id = `${scope}-${field.name}`

  if (field.type === 'bool') {
    return <FieldInput field={{ label: field.label, type: 'boolean' }} value={value} onChange={onChange} />
  }

  return (
    <div className="flex flex-col gap-1">
      <label htmlFor={id} className="text-sm font-medium text-zinc-700">
        {field.label}
        {field.required && <span className="ml-1 text-critical-700">*</span>}
      </label>

      <div className="flex flex-wrap items-center gap-2">
        <input
          id={id}
          type={field.secret ? 'password' : 'text'}
          value={value}
          disabled={field.secret && !sealingConfigured}
          onChange={(event) => onChange(event.target.value)}
          placeholder={
            // Un champ sensible déjà renseigné ne montre pas sa valeur mais dit
            // qu'il en porte une : sans cela, un champ vide serait indiscernable
            // d'un champ jamais rempli, et on le ressaisirait par précaution.
            field.secret && stored ? 'enregistrée — laisser vide pour ne pas la changer' : field.placeholder
          }
          className="min-w-64 flex-1 rounded-md border border-zinc-300 px-2 py-1 text-sm text-zinc-900 disabled:bg-zinc-100"
        />

        {field.secret && stored && (
          <button
            type="button"
            onClick={onClearSecret}
            className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100"
          >
            Effacer
          </button>
        )}
      </div>

      {field.help && <p className="text-xs text-zinc-500">{field.help}</p>}
    </div>
  )
}
