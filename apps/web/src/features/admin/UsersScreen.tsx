import { useState } from 'react'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Icon } from '../../components/Icon'
import { Card, EmptyState, ErrorState, Skeleton } from '../../components/primitives'
import { formatDateTime } from '../../lib/format'
import { useSession, type Role } from '../auth/session'
import {
  ROLE_LABEL,
  useGrantRole,
  useProjects,
  useRevokeRole,
  useRoles,
  type Project,
  type RoleCatalogue,
  type UserRole,
} from './api'

/*
 * Utilisateurs : qui a quel rôle, et comment en attribuer un.
 *
 * Le journal d'audit vivait sur la même page, et ce n'était pas un raccourci de
 * mise en page : il sert d'abord à contrôler les attributions. Il est désormais
 * dans l'onglet voisin — à un clic, ce qui suffit — plutôt qu'empilé sous un
 * formulaire qu'on n'ouvre que rarement.
 */
export function UsersScreen() {
  const { data, isPending, error, refetch } = useRoles()

  return (
    <div className="flex flex-col gap-4">
      {isPending && <Skeleton className="h-64 w-full" />}
      {error && (
        <ErrorState message="Les rôles n'ont pas pu être chargés." onRetry={() => void refetch()} />
      )}

      {data && (
        <>
          <RolesCard roles={data.roles} catalogue={data.catalogue} />
          <GrantCard catalogue={data.catalogue} />
        </>
      )}
    </div>
  )
}

function RolesCard({ roles, catalogue }: { roles: UserRole[]; catalogue: RoleCatalogue[] }) {
  const session = useSession()
  const revoke = useRevokeRole()
  const [target, setTarget] = useState<UserRole | null>(null)

  // Les globaux seuls : `admin` n'existe pas par projet, mais compter large
  // ferait croire à tort qu'il reste un administrateur de secours.
  const admins = roles.filter((role) => role.role === 'admin' && !role.project).length
  const me = session.kind === 'authenticated' ? session.identity.subject : ''

  return (
    <Card title={`Rôles attribués (${roles.length})`}>
      {revoke.isError && <p className="mb-3 text-sm text-critical-700">{(revoke.error as Error).message}</p>}

      {roles.length === 0 ? (
        <EmptyState>Aucun rôle attribué.</EmptyState>
      ) : (
        <ul className="flex flex-col">
          {roles.map((role) => {
            // Le dernier administrateur ne peut pas être retiré : le serveur
            // refuse, et le bouton le dit avant le clic plutôt qu'après.
            const isLastAdmin = role.role === 'admin' && admins <= 1
            return (
              <li
                key={`${role.subject}:${role.project ?? ''}`}
                className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-zinc-200 py-2 text-sm first:border-t-0"
              >
                <span className="min-w-40 font-medium text-zinc-900">{role.username || role.subject}</span>
                <span className="font-mono text-xs text-zinc-500">{role.subject}</span>
                <span className="rounded bg-zinc-100 px-1.5 py-0.5 text-xs text-zinc-700">
                  {ROLE_LABEL[role.role]}
                </span>

                {/* La portée n'est montrée que lorsqu'elle restreint : « partout »
                    sur chaque ligne globale serait du bruit sur l'immense
                    majorité d'entre elles. */}
                {role.project && (
                  <span className="rounded bg-accent-100 px-1.5 py-0.5 text-xs text-accent-700">
                    {role.project_label || role.project}
                  </span>
                )}

                {role.subject === me && <span className="text-xs text-zinc-500">(vous)</span>}

                {role.granted_by === 'bootstrap' && (
                  // Retirer un rôle d'amorçage sans toucher à la configuration
                  // le fait réapparaître au redémarrage : le dire évite de
                  // croire à un bug.
                  <span className="flex items-center gap-1 text-xs text-warn-700">
                    <Icon name="alert" className="size-3.5" />
                    amorçage — redéfini au démarrage
                  </span>
                )}

                {role.last_seen ? (
                  <span className="text-xs text-zinc-500">vu {formatDateTime(role.last_seen)}</span>
                ) : (
                  // Un rôle attribué à un identifiant erroné ne se voit qu'à
                  // ceci : personne ne s'en est jamais servi.
                  <span className="text-xs text-warn-700">jamais connecté</span>
                )}

                {role.sessions > 0 && (
                  <span className="text-xs text-zinc-500">{role.sessions} session(s)</span>
                )}

                <button
                  type="button"
                  onClick={() => setTarget(role)}
                  disabled={isLastAdmin || revoke.isPending}
                  title={
                    isLastAdmin
                      ? "Dernier administrateur : le retirer laisserait le dashboard sans personne pour attribuer de rôle"
                      : undefined
                  }
                  className="ml-auto rounded-md border border-zinc-300 bg-white px-2 py-0.5 text-xs font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-50"
                >
                  Retirer
                </button>
              </li>
            )
          })}
        </ul>
      )}

      {target && (
        <ConfirmDialog
          title="Retirer ce rôle ?"
          confirmLabel="Retirer"
          pending={revoke.isPending}
          /* Se retirer soi-même exige la ressaisie : c'est le geste qu'on ne
             peut pas défaire seul, puisqu'il faudrait le rôle qu'on vient de
             perdre pour le reprendre. */
          challenge={target.subject === me ? target.subject : undefined}
          onCancel={() => setTarget(null)}
          onConfirm={() => {
            revoke.mutate({ subject: target.subject, project: target.project })
            setTarget(null)
          }}
        >
          <p>
            <span className="font-medium text-zinc-900">{target.username || target.subject}</span> perdra le rôle{' '}
            {ROLE_LABEL[target.role]}.
          </p>
          <p className="text-zinc-500">
            Ses {target.sessions} session(s) seront fermées : le changement prend effet immédiatement.
          </p>
          {target.subject === me && (
            <p className="text-critical-700">
              C'est votre propre compte. Vous ne pourrez pas revenir en arrière depuis cette interface.
            </p>
          )}
          {catalogue.length > 0 && target.granted_by === 'bootstrap' && (
            <p className="text-warn-700">
              Ce rôle vient de la configuration d'amorçage : il sera réattribué au prochain démarrage.
            </p>
          )}
        </ConfirmDialog>
      )}
    </Card>
  )
}

function GrantCard({ catalogue }: { catalogue: RoleCatalogue[] }) {
  const grant = useGrantRole()
  const projects = useProjects()
  const [subject, setSubject] = useState('')
  const [username, setUsername] = useState('')
  const [role, setRole] = useState<Role>('viewer')
  const [project, setProject] = useState('')

  const selected = catalogue.find((entry) => entry.role === role)
  // `admin` gouverne les comptes, les dépôts et la synchronisation : rien de
  // tout cela n'appartient à un projet. Le serveur refuse ; le formulaire
  // n'offre pas le choix plutôt que de laisser produire une erreur.
  const scopable = role !== 'admin'

  return (
    <Card title="Attribuer un rôle">
      <form
        className="flex flex-col gap-3"
        onSubmit={(event) => {
          event.preventDefault()
          if (!subject.trim()) return
          grant.mutate({
            subject: subject.trim(),
            username: username.trim(),
            project: scopable && project ? project : undefined,
            role,
          })
        }}
      >
        <div className="flex flex-wrap items-end gap-3">
          <label className="flex flex-col gap-1 text-sm">
            <span className="text-zinc-700">Sujet</span>
            <input
              value={subject}
              onChange={(event) => setSubject(event.target.value)}
              placeholder="identifiant du fournisseur"
              className="w-56 rounded-md border border-zinc-300 bg-white px-2 py-1 font-mono text-sm text-zinc-900"
            />
          </label>

          <label className="flex flex-col gap-1 text-sm">
            <span className="text-zinc-700">Nom affiché</span>
            <input
              value={username}
              onChange={(event) => setUsername(event.target.value)}
              placeholder="facultatif"
              className="w-40 rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm text-zinc-900"
            />
          </label>

          <label className="flex flex-col gap-1 text-sm">
            <span className="text-zinc-700">Rôle</span>
            <select
              value={role}
              onChange={(event) => setRole(event.target.value as Role)}
              className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm text-zinc-900"
            >
              {catalogue.map((entry) => (
                <option key={entry.role} value={entry.role}>
                  {ROLE_LABEL[entry.role]}
                </option>
              ))}
            </select>
          </label>

          {/* La portée. Par défaut « partout », ce que signifient toutes les
              attributions antérieures et ce qui reste le cas courant. Un projet
              nommé restreint le rôle à lui seul — et l'emporte alors sur le rôle
              global de la personne, y compris pour la restreindre. */}
          <label className="flex flex-col gap-1 text-sm">
            <span className="text-zinc-700">Portée</span>
            <select
              value={project}
              disabled={!scopable}
              onChange={(event) => setProject(event.target.value)}
              title={scopable ? undefined : "Le rôle d'administrateur vaut nécessairement partout"}
              className="w-56 rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm text-zinc-900 disabled:opacity-50"
            >
              <option value="">Partout</option>
              {(projects.data?.projects ?? []).map((entry: Project) => (
                <option key={entry.slug} value={entry.slug}>
                  {entry.label || entry.slug}
                </option>
              ))}
            </select>
          </label>

          <button
            type="submit"
            disabled={!subject.trim() || grant.isPending}
            className="rounded-md border border-zinc-300 bg-white px-3 py-1 text-sm font-medium text-zinc-700 transition-colors hover:bg-zinc-100 disabled:opacity-50"
          >
            Attribuer
          </button>
        </div>

        {/* Le sujet, et non le nom : c'est lui qui apparaît dans le jeton et
            porte l'autorisation. Un nom change, un sujet non. */}
        <p className="text-sm text-zinc-500">
          Le sujet est l'identifiant que le fournisseur d'identité met dans le jeton, pas le nom affiché. Attribuer un
          rôle à un compte déjà connecté ferme ses sessions : le changement prend effet à sa prochaine connexion.
        </p>

        {/* Ce qu'un rôle permet vient du serveur : le décrire ici ferait diverger
            la description de la règle au premier ajout de permission. */}
        {selected && (
          <p className="text-sm text-zinc-600">
            {ROLE_LABEL[selected.role]} : {selected.permissions.join(', ')}
          </p>
        )}

        {grant.isError && <p className="text-sm text-critical-700">{(grant.error as Error).message}</p>}
        {grant.isSuccess && <p className="text-sm text-zinc-500">Rôle attribué.</p>}
      </form>
    </Card>
  )
}
