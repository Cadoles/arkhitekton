package auth

import "sort"

/*
 * What a person may do, from their own roles and their groups'.
 *
 * At each scope (the instance, or one project), a role granted to the person wins over their
 * groups': it is what lets a member of a team be restricted. Failing one, the highest role among
 * their groups'. Then, as without groups, a project's role wins over the instance's, with one
 * exception: a group's role in a project applies only when it raises the person above what holds
 * for them across the instance. Groups open access, they never take it away: a whole organisation
 * made viewer of a project must not demote its administrators there.
 */

// Grant is a role at a scope, given to a person (Group empty) or to a group.
type Grant struct {
	// ProjectID is nil for the whole instance.
	ProjectID *int64
	Role      Role
	Group     string
}

// Effective is the role that applies at a scope, and where it comes from: empty for the person's
// own, a group's name otherwise.
type Effective struct {
	Role   Role
	Source string
}

// Access is what a person may do: at the instance's scope, and per project where it differs.
type Access struct {
	Global   Effective
	Projects map[int64]Effective
}

// None says whether nothing at all is granted.
func (a Access) None() bool { return a.Global.Role == "" && len(a.Projects) == 0 }

// Resolve combines a person's own grants and the grants of the groups they belong to. The
// groups' grants are those of their groups only; an unknown role is ignored.
func Resolve(personal, groups []Grant) Access {
	access := Access{Projects: map[int64]Effective{}}
	own := map[int64]bool{}
	ownGlobal := false
	for _, grant := range personal {
		if !grant.Role.Valid() {
			continue
		}
		if grant.ProjectID == nil {
			access.Global, ownGlobal = Effective{Role: grant.Role}, true
			continue
		}
		access.Projects[*grant.ProjectID] = Effective{Role: grant.Role}
		own[*grant.ProjectID] = true
	}

	// The same order every time: between two groups of the same rank, the first by name.
	sorted := append([]Grant(nil), groups...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Group < sorted[j].Group })
	for _, grant := range sorted {
		if !grant.Role.Valid() {
			continue
		}
		candidate := Effective{Role: grant.Role, Source: grant.Group}
		if grant.ProjectID == nil {
			if !ownGlobal && grant.Role.rank() > access.Global.Role.rank() {
				access.Global = candidate
			}
			continue
		}
		id := *grant.ProjectID
		if own[id] {
			continue
		}
		if current, found := access.Projects[id]; !found || grant.Role.rank() > current.Role.rank() {
			access.Projects[id] = candidate
		}
	}
	for id, effective := range access.Projects {
		if effective.Source != "" && effective.Role.rank() <= access.Global.Role.rank() {
			delete(access.Projects, id)
		}
	}
	return access
}
