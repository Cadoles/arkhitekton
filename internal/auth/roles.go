// Package auth porte l'authentification OIDC et l'autorisation du dashboard.
package auth

import (
	"fmt"
	"slices"
	"strings"
)

// Role décrit ce qu'un utilisateur peut faire. Les rôles sont ordonnés : chacun
// inclut les permissions du précédent.
type Role string

const (
	RoleViewer   Role = "viewer"
	RoleOperator Role = "operator"
	RoleReleaser Role = "releaser"
	RoleAdmin    Role = "admin"
)

// AllRoles est ordonné du moins au plus permissif.
var AllRoles = []Role{RoleViewer, RoleOperator, RoleReleaser, RoleAdmin}

func (r Role) Valid() bool { return slices.Contains(AllRoles, r) }

// rank permet de comparer deux rôles. Un rôle inconnu vaut -1 : il ne satisfait
// donc aucune exigence, pas même celle du rôle le plus faible.
func (r Role) rank() int { return slices.Index(AllRoles, r) }

// Permission nomme une capacité. Le déploiement est indexé par environnement
// (`deploy:prod`) : ajouter un environnement ne doit pas demander de modifier le
// modèle de permissions.
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

// PermDeploy construit la permission de déploiement d'un environnement.
func PermDeploy(environment string) Permission {
	return Permission(deployPrefix + environment)
}

// rolePermissions associe à chaque rôle ce qu'il ajoute au précédent.
//
// Le découpage en quatre rôles anticipe une distinction qui apparaît toujours à
// l'usage : lancer une campagne de tests sur `unstable` et promouvoir en
// production ne sont pas le même geste, et ne concernent pas les mêmes personnes.
// Si le client n'en veut que deux, `operator` et `releaser` se confondent par
// configuration, sans changement de code.
var rolePermissions = map[Role][]Permission{
	RoleViewer: {PermRead, PermReleaseExport},
	RoleOperator: {
		PermPipelineRun, PermPipelineCancel,
		PermReleaseCreate, PermReleaseEdit,
	},
	RoleReleaser: {PermReleaseFinal},
	RoleAdmin:    {PermAdmin},
}

// Identity est l'utilisateur authentifié, tel que le middleware le pose dans le
// contexte de la requête.
type Identity struct {
	Subject  string
	Username string
	Email    string
	// Projects porte les rôles accordés **par projet**, indexés par identifiant.
	// Vide dans le cas courant : la plupart des attributions valent partout.
	//
	// Un rôle de projet **remplace** le rôle global sur ce projet, dans les deux
	// sens. C'est ce qui rend une restriction exprimable — accorder `viewer` sur
	// un projet à quelqu'un qui est `operator` ailleurs — et une règle qui prendrait
	// le plus élevé des deux ne le permettrait pas. Le plus spécifique gagne.
	Projects map[int64]Role
	Role     Role
}

// ProjectPolicies porte les exigences de promotion, par slug de projet puis par
// identifiant d'environnement. Deux projets peuvent avoir chacun un `qualif`, et
// l'un le déclarer sensible sans que l'autre le soit (doc 16 §16.18).
type ProjectPolicies map[string]map[string]EnvironmentPolicy

// EnvironmentPolicy décrit les exigences de promotion d'un environnement.
type EnvironmentPolicy struct {
	// Sensitive impose le rôle `releaser`. Vaut pour la pré-production et la
	// production, qui reçoivent ce qui part chez l'utilisateur final.
	Sensitive bool
}

// Authorizer décide si une identité peut exercer une permission.
type Authorizer struct {
	environments ProjectPolicies
}

// NewAuthorizer construit l'autorisateur à partir des environnements déclarés.
//
// Un environnement absent de cette table est traité comme sensible : un
// environnement nouvellement créé et oublié dans la configuration doit être plus
// difficile à atteindre, jamais plus facile. Se tromper dans ce sens fait perdre
// du temps ; se tromper dans l'autre laisse promouvoir en production.
func NewAuthorizer(environments ProjectPolicies) *Authorizer {
	return &Authorizer{environments: environments}
}

// Sensitive dit si un environnement reçoit ce qui part chez l'utilisateur final.
//
// Exposé parce que l'interface en a besoin : c'est ce qui déclenche la ressaisie
// de la version à la confirmation (doc 08 §8.5). Le déduire côté navigateur — de
// l'observabilité, du rang — reviendrait à traiter une coïncidence comme une
// règle : la production est aujourd'hui non observable, mais c'est sa sensibilité
// qui justifie la friction, pas son isolement.
//
// Un environnement absent de la table est sensible, comme pour l'autorisation :
// se tromper dans ce sens fait perdre du temps, dans l'autre laisse promouvoir
// sans y penser.
func (a *Authorizer) Sensitive(project, environment string) bool {
	policy, known := a.environments[project][environment]
	return !known || policy.Sensitive
}

// CanDeploy dit si l'identité peut promouvoir vers un environnement d'un projet.
//
// Le rôle est celui du projet, la sensibilité celle que le projet déclare. Le
// projet est désigné deux fois — par son identifiant pour le rôle, par son slug
// pour la déclaration — parce que ce sont les deux clés sous lesquelles chacune
// est tenue.
func (a *Authorizer) CanDeploy(identity Identity, projectID int64, project, environment string) bool {
	role := identity.RoleIn(projectID)
	if !role.Valid() {
		return false
	}
	return role.rank() >= a.minRoleForDeploy(project, environment).rank()
}

// PermissionsOf rend les permissions cumulées d'un rôle.
//
// Cumulées, parce que les rôles sont ordonnés : `releaser` ajoute aux droits de
// `operator`, qui ajoute à ceux de `viewer`. Une interface qui n'afficherait que
// l'apport d'un rôle laisserait croire qu'un releaser ne peut pas lancer de
// pipeline.
//
// Exposé pour que l'écran d'administration décrive ce qu'il attribue. Le
// dupliquer côté navigateur ferait diverger la description de la règle au
// premier ajout de permission.
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

// CanIn décide dans le périmètre d'un projet.
//
// Le rôle du projet quand il y en a un, le rôle global sinon. Distincte de [Can]
// et non un paramètre optionnel : une permission vérifiée sans projet là où il en
// faudrait un passerait inaperçue, alors qu'un appel au mauvais nom se voit.
func (a *Authorizer) CanIn(identity Identity, projectID int64, permission Permission) bool {
	return a.allows(identity.RoleIn(projectID), permission)
}

// RoleIn rend le rôle effectif dans un projet.
//
// Le plus spécifique gagne, y compris pour restreindre : un `viewer` accordé sur
// un projet l'emporte sur un `operator` global. Sans cela, une restriction serait
// inexprimable, et il faudrait retirer le rôle global pour la poser.
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
		// Sans projet, la sensibilité n'est pas connue : l'exigence la plus forte
		// s'applique. [Authorizer.CanDeploy] est le chemin qui la connaît.
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

// minRoleForDeploy rend le rôle minimal requis pour promouvoir sur un
// environnement. Inconnu ou sensible : `releaser`. Connu et non sensible :
// `operator`.
func (a *Authorizer) minRoleForDeploy(project, environment string) Role {
	policy, known := a.environments[project][environment]
	if !known || policy.Sensitive {
		return RoleReleaser
	}
	return RoleOperator
}

// ParseRole valide un rôle venu de la base ou d'une saisie d'administration.
func ParseRole(value string) (Role, error) {
	role := Role(strings.TrimSpace(strings.ToLower(value)))
	if !role.Valid() {
		return "", fmt.Errorf("rôle inconnu %q (attendu: viewer, operator, releaser, admin)", value)
	}
	return role, nil
}
