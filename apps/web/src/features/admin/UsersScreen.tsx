import { useMemo, useState, type ReactNode } from 'react'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Icon } from '../../components/Icon'
import { Card, EmptyState, ErrorState, Skeleton } from '../../components/primitives'
import { Select } from '../../components/Select'
import { formatDateTime } from '../../lib/format'
import { useSession, type Role } from '../auth/session'
import { ROLE_LABEL, useProjects, type Project } from './api'
import {
  PERMISSIONS,
  useClearGroupRole,
  useGrantRole,
  useGroups,
  usePeople,
  useRevokeRole,
  useRoleCatalogue,
  useSetGroupRole,
  type Effective,
  type Grant,
  type GroupAccess,
  type Person,
  type RoleCatalogue,
} from './access'

/*
 * Utilisateurs : qui peut se servir du dashboard, et pourquoi.
 *
 * Une ligne par personne, et non par rôle attribué : ce qu'on vient chercher
 * est « que peut faire Alice ? », et la réponse tient à ses propres rôles comme
 * à ceux de ses groupes. Les personnes connectées sans accès viennent en tête :
 * c'est le plus souvent pour elles qu'on ouvre l'écran.
 *
 * Un rôle se règle sur place, depuis la fiche dépliée de la personne ou du
 * groupe. Les permissions viennent du serveur ; l'écran ne fait que les nommer.
 */
export function UsersScreen() {
  return (
    <div className="flex flex-col gap-4">
      <PeopleCard />
      <GroupsCard />
      <RolesCard />
    </div>
  )
}

const ROLES: Role[] = ['viewer', 'operator', 'releaser', 'admin']

function roleText(effective: Effective): string {
  if (!effective.role) return 'Aucun accès'
  return effective.source ? `${ROLE_LABEL[effective.role]} via ${effective.source}` : ROLE_LABEL[effective.role]
}

function RoleBadge({ effective }: { effective: Effective }) {
  return (
    <span
      className={`rounded px-1.5 py-0.5 text-xs ${
        effective.role ? 'bg-zinc-100 text-zinc-700' : 'bg-warn-50 text-warn-700'
      }`}
    >
      {roleText(effective)}
    </span>
  )
}

function PeopleCard() {
  const { data, isPending, error, refetch } = usePeople()
  const [search, setSearch] = useState('')
  const [open, setOpen] = useState('')

  const people = useMemo(() => {
    const needle = search.trim().toLowerCase()
    const all = data?.people ?? []
    if (!needle) return all
    return all.filter((person) =>
      [person.username, person.email, person.subject, ...person.groups].some((value) =>
        value?.toLowerCase().includes(needle),
      ),
    )
  }, [data, search])

  // Les administrateurs propres et globaux : un groupe peut se vider à la connexion
  // suivante, il ne garantit pas qu'il en reste un.
  const admins = (data?.people ?? []).filter((person) =>
    person.roles.some((grant) => grant.role === 'admin' && !grant.project),
  ).length
  const pending = (data?.people ?? []).filter((person) => person.pending).length

  return (
    <Card
      title={`Personnes (${data?.people.length ?? 0})`}
      action={
        <input
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          placeholder="nom, courriel, groupe…"
          aria-label="Rechercher une personne"
          className="rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-xs text-zinc-900"
        />
      }
    >
      {pending > 0 && (
        <p className="mb-3 flex items-center gap-1.5 text-sm text-warn-700">
          <Icon name="clock" className="size-4" />
          {pending} personne(s) connectée(s) sans accès, en attente d&apos;un rôle.
        </p>
      )}
      {isPending && <Skeleton className="h-32 w-full" />}
      {error && <ErrorState message="Les personnes n'ont pas pu être chargées." onRetry={() => void refetch()} />}
      {data &&
        (people.length === 0 ? (
          <EmptyState>Personne ne correspond.</EmptyState>
        ) : (
          <ul className="flex flex-col divide-y divide-zinc-200">
            {people.map((person) => (
              <PersonRow
                key={person.subject}
                person={person}
                admins={admins}
                open={open === person.subject}
                onToggle={() => setOpen(open === person.subject ? '' : person.subject)}
              />
            ))}
          </ul>
        ))}
      <AddBySubject />
    </Card>
  )
}

function PersonRow({
  person,
  admins,
  open,
  onToggle,
}: {
  person: Person
  admins: number
  open: boolean
  onToggle: () => void
}) {
  const session = useSession()
  const me = session.kind === 'authenticated' ? session.identity.subject : ''

  return (
    <li className="py-2">
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="flex w-full flex-wrap items-center gap-x-3 gap-y-1 text-left text-sm"
      >
        <Icon name="chevron" className={`size-4 shrink-0 text-zinc-400 transition-transform ${open ? '' : '-rotate-90'}`} />
        <span className="min-w-40 font-medium text-zinc-900">
          {person.username || person.subject}
          {person.subject === me && <span className="ml-1 text-xs font-normal text-zinc-500">(vous)</span>}
        </span>
        {person.pending ? (
          <span className="rounded bg-warn-50 px-1.5 py-0.5 text-xs text-warn-700">En attente</span>
        ) : (
          <RoleBadge effective={person.global} />
        )}
        {person.projects.map((project) => (
          <span key={project.project} className="rounded bg-accent-100 px-1.5 py-0.5 text-xs text-accent-700">
            {project.project_label || project.project} : {roleText(project)}
          </span>
        ))}
        {person.groups.length > 0 && (
          <span className="text-xs text-zinc-500">
            {person.groups.length} groupe{person.groups.length > 1 ? 's' : ''}
          </span>
        )}
        <span className="ml-auto text-xs text-zinc-500">
          {person.last_seen ? `vu le ${formatDateTime(person.last_seen)}` : 'jamais connecté'}
        </span>
      </button>
      {open && <PersonSheet person={person} admins={admins} me={me} />}
    </li>
  )
}

/** La fiche d'une personne : ses propres rôles, réglés sur place. */
function PersonSheet({ person, admins, me }: { person: Person; admins: number; me: string }) {
  const grant = useGrantRole()
  const revoke = useRevokeRole()
  const [confirm, setConfirm] = useState<null | { text: string; challenge?: string; run: () => void }>(null)

  const own = (project?: string) => person.roles.find((role) => (role.project ?? '') === (project ?? ''))
  const ownGlobal = own()
  const lastAdmin = ownGlobal?.role === 'admin' && admins <= 1
  const self = person.subject === me
  const busy = grant.isPending || revoke.isPending

  // Changer un rôle ferme ses sessions. Le faire sur soi se confirme en ressaisissant
  // son identifiant : c'est le geste qu'on ne peut pas défaire seul.
  const change = (role: Role | '', project?: string) => {
    const run = () =>
      role
        ? grant.mutate({ subject: person.subject, username: person.username, role, project })
        : revoke.mutate({ subject: person.subject, project })
    if (self) {
      setConfirm({ text: 'Vous modifiez votre propre accès.', challenge: person.subject, run })
      return
    }
    run()
  }

  return (
    <div className="mt-2 ml-6 flex flex-col gap-3 rounded-md border border-zinc-200 p-3 text-sm">
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-xs">
        <dt className="text-zinc-500">Identifiant</dt>
        <dd className="font-mono text-zinc-700">{person.subject}</dd>
        {person.email && (
          <>
            <dt className="text-zinc-500">Courriel</dt>
            <dd className="text-zinc-700">{person.email}</dd>
          </>
        )}
        <dt className="text-zinc-500">Groupes</dt>
        <dd className="text-zinc-700">{person.groups.length > 0 ? person.groups.join(', ') : 'aucun'}</dd>
        <dt className="text-zinc-500">Sessions</dt>
        <dd className="text-zinc-700">{person.sessions}</dd>
      </dl>

      <ScopeEditor
        label="Toute l'instance"
        own={ownGlobal}
        effective={person.global}
        roles={ROLES}
        none="Aucun (hérite des groupes)"
        disabled={busy}
        lockedReason={lastAdmin ? "Dernier administrateur : le retirer laisserait le dashboard sans personne pour attribuer de rôle" : undefined}
        onChange={(role) => change(role)}
      />
      <ProjectScopes
        grants={person.roles}
        effective={person.projects}
        global={person.global}
        disabled={busy}
        onChange={(role, project) => change(role, project)}
      />

      <p className="text-xs text-zinc-500">
        Un changement ferme ses sessions : il prend effet tout de suite. Un rôle de groupe ne s&apos;applique que là où la
        personne n&apos;a pas le sien.
      </p>
      {(grant.isError || revoke.isError) && (
        <p className="text-sm text-critical-700">{((grant.error ?? revoke.error) as Error).message}</p>
      )}

      {person.roles.length > 0 && !lastAdmin && (
        <button
          type="button"
          disabled={busy}
          onClick={() =>
            setConfirm({
              text: `${person.username || person.subject} perdra tous ses rôles propres ; ceux de ses groupes restent.`,
              challenge: self ? person.subject : undefined,
              run: () => {
                // L'un après l'autre : chaque retrait ferme les sessions, et le dernier
                // administrateur reste protégé par le serveur.
                void (async () => {
                  for (const role of person.roles) await revoke.mutateAsync({ subject: person.subject, project: role.project })
                })()
              },
            })
          }
          className="w-fit rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-50"
        >
          Retirer tous ses rôles
        </button>
      )}

      {confirm && (
        <ConfirmDialog
          title="Modifier cet accès ?"
          confirmLabel="Confirmer"
          challenge={confirm.challenge}
          onCancel={() => setConfirm(null)}
          onConfirm={() => {
            confirm.run()
            setConfirm(null)
          }}
        >
          <p>{confirm.text}</p>
          <p className="text-zinc-500">Ses {person.sessions} session(s) seront fermées.</p>
          {self && <p className="text-critical-700">C&apos;est votre propre compte.</p>}
        </ConfirmDialog>
      )}
    </div>
  )
}

/** Le réglage d'un rôle à une portée : le sien, et ce qui s'applique au bout du compte. */
function ScopeEditor({
  label,
  own,
  effective,
  roles,
  none,
  disabled,
  lockedReason,
  onChange,
}: {
  label: ReactNode
  own?: Grant
  effective: Effective
  roles: Role[]
  none: string
  disabled: boolean
  lockedReason?: string
  onChange: (role: Role | '') => void
}) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <span className="w-44 text-zinc-700">{label}</span>
      <Select
        size="xs"
        value={own?.role ?? ''}
        disabled={disabled || Boolean(lockedReason)}
        title={lockedReason}
        aria-label={`Rôle propre : ${typeof label === 'string' ? label : ''}`}
        onChange={(event) => onChange(event.target.value as Role | '')}
      >
        <option value="">{none}</option>
        {roles.map((role) => (
          <option key={role} value={role}>
            {ROLE_LABEL[role]}
          </option>
        ))}
      </Select>
      <span className="text-xs text-zinc-500">s&apos;applique : {roleText(effective)}</span>
      {own?.bootstrap && (
        <span className="flex items-center gap-1 text-xs text-warn-700">
          <Icon name="alert" className="size-3.5" />
          attribué au démarrage
        </span>
      )}
    </div>
  )
}

/** Les projets : « hérite » du global, ou un rôle propre au projet. */
function ProjectScopes({
  grants,
  effective,
  global,
  disabled,
  onChange,
}: {
  grants: Grant[]
  effective: Effective[]
  global: Effective
  disabled: boolean
  onChange: (role: Role | '', project: string) => void
}) {
  const projects = useProjects()
  const list: Project[] = projects.data?.projects ?? []
  if (list.length === 0) return null

  return (
    <div className="flex flex-col gap-1.5 border-t border-zinc-200 pt-2">
      {list.map((project) => (
        <ScopeEditor
          key={project.slug}
          label={project.label || project.slug}
          own={grants.find((grant) => grant.project === project.slug)}
          effective={effective.find((entry) => entry.project === project.slug) ?? global}
          roles={ROLES.filter((role) => role !== 'admin')}
          none="Hérite"
          disabled={disabled}
          onChange={(role) => onChange(role, project.slug)}
        />
      ))}
    </div>
  )
}

/** Pour quelqu'un qui ne s'est jamais connecté : son identifiant chez le fournisseur. */
function AddBySubject() {
  const grant = useGrantRole()
  const [subject, setSubject] = useState('')
  const [username, setUsername] = useState('')
  const [role, setRole] = useState<Role>('viewer')

  return (
    <details className="mt-3 border-t border-zinc-200 pt-3 text-sm">
      <summary className="cursor-pointer text-zinc-600">Ajouter quelqu&apos;un qui ne s&apos;est jamais connecté</summary>
      <form
        className="mt-2 flex flex-wrap items-end gap-2"
        onSubmit={(event) => {
          event.preventDefault()
          if (subject.trim()) grant.mutate({ subject: subject.trim(), username: username.trim(), role })
        }}
      >
        <input
          value={subject}
          onChange={(event) => setSubject(event.target.value)}
          placeholder="identifiant chez le fournisseur"
          aria-label="Identifiant chez le fournisseur"
          className="w-56 rounded-md border border-zinc-300 bg-white px-2 py-1 font-mono text-sm text-zinc-900"
        />
        <input
          value={username}
          onChange={(event) => setUsername(event.target.value)}
          placeholder="nom affiché (facultatif)"
          aria-label="Nom affiché"
          className="w-44 rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm text-zinc-900"
        />
        <Select value={role} aria-label="Rôle" onChange={(event) => setRole(event.target.value as Role)}>
          {ROLES.map((entry) => (
            <option key={entry} value={entry}>
              {ROLE_LABEL[entry]}
            </option>
          ))}
        </Select>
        <button
          type="submit"
          disabled={!subject.trim() || grant.isPending}
          className="rounded-md border border-zinc-300 bg-white px-3 py-1 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-50"
        >
          Attribuer
        </button>
        <p className="w-full text-xs text-zinc-500">
          Le plus simple reste qu&apos;elle se connecte une fois : elle apparaît alors en attente, ou reçoit le rôle de
          ses groupes.
        </p>
        {grant.isError && <p className="w-full text-sm text-critical-700">{(grant.error as Error).message}</p>}
      </form>
    </details>
  )
}

function GroupsCard() {
  const { data, isPending, error, refetch } = useGroups()
  const setRole = useSetGroupRole()
  const [open, setOpen] = useState('')
  const [group, setGroup] = useState('')
  const [role, setNewRole] = useState<Role>('viewer')

  const granted = data?.groups ?? []
  const suggestions = (data?.known ?? []).filter((known) => !granted.some((entry) => entry.group === known.group))

  return (
    <Card title={`Groupes (${granted.length})`}>
      <p className="mb-3 text-sm text-zinc-600">
        Un groupe est une organisation ou une équipe de la forge, que le fournisseur d&apos;identité met dans le jeton.
        Son rôle vaut pour tous ses membres là où ils n&apos;en ont pas un à eux ; sur un projet, il ne s&apos;applique
        que s&apos;il élève : un groupe ouvre des accès, il n&apos;en retire jamais. Entrer dans un groupe ou en sortir
        prend effet à la connexion suivante.
      </p>
      {isPending && <Skeleton className="h-16 w-full" />}
      {error && <ErrorState message="Les groupes n'ont pas pu être chargés." onRetry={() => void refetch()} />}
      {data &&
        (granted.length === 0 ? (
          <EmptyState>Aucun groupe n&apos;a de rôle.</EmptyState>
        ) : (
          <ul className="flex flex-col divide-y divide-zinc-200">
            {granted.map((entry) => (
              <GroupRow
                key={entry.group}
                group={entry}
                open={open === entry.group}
                onToggle={() => setOpen(open === entry.group ? '' : entry.group)}
              />
            ))}
          </ul>
        ))}

      <form
        className="mt-3 flex flex-wrap items-center gap-2 border-t border-zinc-200 pt-3"
        onSubmit={(event) => {
          event.preventDefault()
          if (!group.trim()) return
          setRole.mutate({ group: group.trim(), role }, { onSuccess: () => setGroup('') })
        }}
      >
        <input
          value={group}
          onChange={(event) => setGroup(event.target.value)}
          list="known-groups"
          placeholder="organisation ou organisation:équipe"
          aria-label="Groupe"
          className="w-64 rounded-md border border-zinc-300 bg-white px-2 py-1 font-mono text-sm text-zinc-900"
        />
        <datalist id="known-groups">
          {suggestions.map((known) => (
            <option key={known.group} value={known.group}>
              {known.members} compte(s)
            </option>
          ))}
        </datalist>
        <Select value={role} aria-label="Rôle du groupe" onChange={(event) => setNewRole(event.target.value as Role)}>
          {ROLES.map((entry) => (
            <option key={entry} value={entry}>
              {ROLE_LABEL[entry]}
            </option>
          ))}
        </Select>
        <button
          type="submit"
          disabled={!group.trim() || setRole.isPending}
          className="rounded-md border border-zinc-300 bg-white px-3 py-1 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-50"
        >
          Donner ce rôle
        </button>
        {suggestions.length > 0 && (
          <span className="text-xs text-zinc-500">{suggestions.length} groupe(s) vu(s) aux connexions, sans rôle.</span>
        )}
        {setRole.isError && <p className="w-full text-sm text-critical-700">{(setRole.error as Error).message}</p>}
      </form>
    </Card>
  )
}

function GroupRow({ group, open, onToggle }: { group: GroupAccess; open: boolean; onToggle: () => void }) {
  const setRole = useSetGroupRole()
  const clear = useClearGroupRole()
  const busy = setRole.isPending || clear.isPending
  const global = group.roles.find((grant) => !grant.project)
  const change = (role: Role | '', project?: string) =>
    role ? setRole.mutate({ group: group.group, role, project }) : clear.mutate({ group: group.group, project })

  return (
    <li className="py-2">
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="flex w-full flex-wrap items-center gap-x-3 gap-y-1 text-left text-sm"
      >
        <Icon name="chevron" className={`size-4 shrink-0 text-zinc-400 transition-transform ${open ? '' : '-rotate-90'}`} />
        <span className="min-w-40 font-mono font-medium text-zinc-900">{group.group}</span>
        {global && <RoleBadge effective={{ role: global.role }} />}
        {group.roles
          .filter((grant) => grant.project)
          .map((grant) => (
            <span key={grant.project} className="rounded bg-accent-100 px-1.5 py-0.5 text-xs text-accent-700">
              {grant.project_label || grant.project} : {ROLE_LABEL[grant.role]}
            </span>
          ))}
        <span className="ml-auto text-xs text-zinc-500">{group.members} compte(s) vu(s)</span>
      </button>
      {open && (
        <div className="mt-2 ml-6 flex flex-col gap-1.5 rounded-md border border-zinc-200 p-3 text-sm">
          <ScopeEditor
            label="Toute l'instance"
            own={global}
            effective={{ role: global?.role ?? '' }}
            roles={ROLES}
            none="Aucun"
            disabled={busy}
            onChange={(role) => change(role)}
          />
          <ProjectScopes
            grants={group.roles}
            effective={group.roles.filter((grant) => grant.project).map((grant) => ({ ...grant }))}
            global={{ role: global?.role ?? '' }}
            disabled={busy}
            onChange={(role, project) => change(role, project)}
          />
          {(setRole.isError || clear.isError) && (
            <p className="text-sm text-critical-700">{((setRole.error ?? clear.error) as Error).message}</p>
          )}
        </div>
      )}
    </li>
  )
}

/** Ce que chaque rôle permet, du serveur : un rôle comprend ceux qui le précèdent. */
function RolesCard() {
  const { data } = useRoleCatalogue()
  const catalogue: RoleCatalogue[] = data?.catalogue ?? []
  if (catalogue.length === 0) return null

  const rows: { label: string; has: (entry: RoleCatalogue) => boolean }[] = [
    ...PERMISSIONS.map(({ permission, label }) => ({
      label,
      has: (entry: RoleCatalogue) => entry.permissions.includes(permission),
    })),
    { label: 'Promouvoir vers un environnement non sensible', has: (entry) => entry.deploy.open },
    { label: 'Promouvoir vers un environnement sensible', has: (entry) => entry.deploy.sensitive },
    { label: 'Administrer (accès, projets, plugins)', has: (entry) => entry.permissions.includes('admin') },
  ]

  return (
    <Card title="Rôles">
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left text-xs text-zinc-500">
              <th className="py-1 pr-3 font-medium">Ce que permet chaque rôle</th>
              {catalogue.map((entry) => (
                <th key={entry.role} className="px-2 py-1 text-center font-medium">
                  {ROLE_LABEL[entry.role]}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.label} className="border-t border-zinc-200">
                <td className="py-1 pr-3 text-zinc-700">{row.label}</td>
                {catalogue.map((entry) => (
                  <td key={entry.role} className="px-2 py-1 text-center">
                    {row.has(entry) ? (
                      <>
                        <Icon name="check" className="mx-auto size-4 text-healthy-700" />
                        <span className="sr-only">oui</span>
                      </>
                    ) : (
                      <span className="text-zinc-300">
                        –<span className="sr-only">non</span>
                      </span>
                    )}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="mt-2 text-xs text-zinc-500">
        Un environnement est sensible quand il reçoit ce qui part chez l&apos;utilisateur final ; le catalogue le
        déclare par projet.
      </p>
    </Card>
  )
}
