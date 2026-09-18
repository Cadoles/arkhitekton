// Package auth carries OIDC authentication and authorization.
// See docs/09-security.md.
package auth

import (
	"fmt"
	"slices"
	"strings"
)

// Role says what a user may do. Roles are ordered: each includes the permissions
// of the previous one.
type Role string

const (
	RoleViewer   Role = "viewer"
	RoleOperator Role = "operator"
	RoleReleaser Role = "releaser"
	RoleAdmin    Role = "admin"
)

// AllRoles is ordered from least to most permissive.
var AllRoles = []Role{RoleViewer, RoleOperator, RoleReleaser, RoleAdmin}

func (r Role) Valid() bool { return slices.Contains(AllRoles, r) }

// rank allows comparing two roles. An unknown role ranks -1: it satisfies no
// requirement, not even the weakest role's.
func (r Role) rank() int { return slices.Index(AllRoles, r) }

// Permission names a capability. Deployment is indexed by environment.
type Permission string

const (
	PermRead           Permission = "read"
	PermPipelineRun    Permission = "pipeline:run"
	PermPipelineCancel Permission = "pipeline:cancel"
	PermReleaseCreate  Permission = "release:create"
	PermReleaseFinal   Permission = "release:create:final"
	PermReleaseEdit    Permission = "release:edit"
	PermReleaseExport  Permission = "release:export"
	PermAdmin          Permission = "admin"
)

const deployPrefix = "deploy:"

// PermDeploy builds the deploy permission of an environment.
func PermDeploy(environment string) Permission {
	return Permission(deployPrefix + environment)
}

// rolePermissions maps each role to what it adds to the previous one.
//
// Four roles anticipate a distinction that always appears in practice: running a
// test campaign and promoting to production are not the same gesture and do not
// concern the same people. A deployment wanting only two can merge operator and
// releaser by configuration, with no code change.
var rolePermissions = map[Role][]Permission{
	RoleViewer: {PermRead, PermReleaseExport},
	RoleOperator: {
		PermPipelineRun, PermPipelineCancel,
		PermReleaseCreate, PermReleaseEdit,
	},
	RoleReleaser: {PermReleaseFinal},
	RoleAdmin:    {PermAdmin},
}

// Identity is the authenticated user, as the middleware puts it in the request
// context.
type Identity struct {
	Subject  string
	Username string
	Email    string
	// Projects carries roles granted per project, keyed by identifier. Empty in the
	// common case: most grants hold everywhere.
	//
	// A project role replaces the global role on that project, in both directions.
	// That is what makes a restriction expressible — granting viewer on one project to
	// someone who is operator elsewhere — which a rule taking the higher of the two
	// could not. The most specific wins.
	Projects map[int64]Role
	Role     Role
}

// ProjectPolicies carries promotion requirements, by project slug then by
// environment id. Two projects may each have a `qualif`, and one may declare it
// sensitive without the other doing so.
type ProjectPolicies map[string]map[string]EnvironmentPolicy

// EnvironmentPolicy describes an environment's promotion requirements.
type EnvironmentPolicy struct {
	// Sensitive requires the releaser role. It holds for environments that receive
	// what reaches the end user.
	Sensitive bool
}

// Authorizer decides whether an identity may exercise a permission.
type Authorizer struct {
	environments ProjectPolicies
}

// NewAuthorizer builds the authorizer from the declared environments.
//
// An environment absent from this table is treated as sensitive: one newly created
// and forgotten in the configuration must be harder to reach, never easier. Erring
// this way costs time; erring the other way lets someone promote to production.
func NewAuthorizer(environments ProjectPolicies) *Authorizer {
	return &Authorizer{environments: environments}
}

// Sensitive says whether an environment receives what reaches the end user.
//
// Exposed because the interface needs it: it is what triggers retyping the version
// on confirmation. Inferring it in the browser — from observability, from rank —
// would treat a coincidence as a rule: production happens to be unobservable, but
// it is its sensitivity that justifies the friction, not its isolation.
//
// An environment absent from the table is sensitive, as for authorization.
func (a *Authorizer) Sensitive(project, environment string) bool {
	policy, known := a.environments[project][environment]
	return !known || policy.Sensitive
}

// CanDeploy says whether the identity may promote to an environment of a project.
//
// The role is the project's, the sensitivity the one the project declares. The
// project is named twice — by identifier for the role, by slug for the declaration
// — because those are the two keys each is held under.
func (a *Authorizer) CanDeploy(identity Identity, projectID int64, project, environment string) bool {
	role := identity.RoleIn(projectID)
	if !role.Valid() {
		return false
	}
	return role.rank() >= a.minRoleForDeploy(project, environment).rank()
}

// PermissionsOf returns a role's cumulative permissions.
//
// Cumulative, because roles are ordered: releaser adds to operator's rights, which
// add to viewer's. An interface showing only a role's own additions would suggest
// a releaser cannot launch a pipeline.
//
// Exposed so the administration screen can describe what it grants. Duplicating it
// in the browser would let the description diverge from the rule at the first
// permission added.
func PermissionsOf(role Role) []Permission {
	if !role.Valid() {
		return nil
	}
	var granted []Permission
	for _, candidate := range AllRoles {
		if candidate.rank() > role.rank() {
			break
		}
		granted = append(granted, rolePermissions[candidate]...)
	}
	return granted
}

// Can indique si l'identité dispose de la permission demandée.
func (a *Authorizer) Can(identity Identity, permission Permission) bool {
	return a.allows(identity.Role, permission)
}

// CanIn decides within a project's scope.
//
// The project role when there is one, the global role otherwise. Distinct from
// [Can] rather than an optional parameter: a permission checked without a project
// where one was needed would go unnoticed, whereas calling the wrong name shows.
func (a *Authorizer) CanIn(identity Identity, projectID int64, permission Permission) bool {
	return a.allows(identity.RoleIn(projectID), permission)
}

// RoleIn returns the effective role in a project.
//
// The most specific wins, including to restrict: a viewer granted on one project
// beats a global operator. Without that, a restriction would be inexpressible and
// the global role would have to be removed to impose one.
func (i Identity) RoleIn(projectID int64) Role {
	if role, found := i.Projects[projectID]; found {
		return role
	}
	return i.Role
}

func (a *Authorizer) allows(role Role, permission Permission) bool {
	if !role.Valid() {
		return false
	}

	if _, isDeploy := strings.CutPrefix(string(permission), deployPrefix); isDeploy {
		// Without a project the sensitivity is unknown: the strongest requirement applies.
		// [Authorizer.CanDeploy] is the path that knows it.
		return role.rank() >= RoleReleaser.rank()
	}

	for _, granted := range AllRoles {
		if granted.rank() > role.rank() {
			break
		}
		if slices.Contains(rolePermissions[granted], permission) {
			return true
		}
	}
	return false
}

// minRoleForDeploy returns the minimum role required to promote to an environment.
// Unknown or sensitive: releaser. Known and not sensitive: operator.
func (a *Authorizer) minRoleForDeploy(project, environment string) Role {
	policy, known := a.environments[project][environment]
	if !known || policy.Sensitive {
		return RoleReleaser
	}
	return RoleOperator
}

// ParseRole validates a role coming from the database or from an administrator's
// input.
func ParseRole(value string) (Role, error) {
	role := Role(strings.TrimSpace(strings.ToLower(value)))
	if !role.Valid() {
		return "", fmt.Errorf("unknown role %q (expected: viewer, operator, releaser, admin)", value)
	}
	return role, nil
}
