package configure

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/configfile"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Access in the versioned configuration: the roles of people, by account name, and of groups.
 *
 * Compared with what is recorded, read from the database by subject: an account declared by its
 * name and recorded under its subject is the same, and a name or a subject must not read as two.
 */

// AccessReadStore is what planning and exporting access read.
type AccessReadStore interface {
	ListAccounts(ctx context.Context) ([]store.Account, error)
	ListUserRoles(ctx context.Context) ([]store.UserRole, error)
	ListGroupRoles(ctx context.Context) ([]store.GroupRole, error)
}

// recordedAccess is what the database holds, by subject and by group.
type recordedAccess struct {
	users     map[string]configfile.Grants
	bootstrap map[string]bool
	groups    map[string]configfile.Grants
	// names maps a subject to the name it is known by; byName, a lower-cased name to its subjects.
	names  map[string]string
	byName map[string][]string
}

func readAccess(ctx context.Context, source AccessReadStore) (recordedAccess, error) {
	recorded := recordedAccess{
		users: map[string]configfile.Grants{}, bootstrap: map[string]bool{},
		groups: map[string]configfile.Grants{}, names: map[string]string{}, byName: map[string][]string{},
	}
	accounts, err := source.ListAccounts(ctx)
	if err != nil {
		return recorded, err
	}
	roles, err := source.ListUserRoles(ctx)
	if err != nil {
		return recorded, err
	}
	groups, err := source.ListGroupRoles(ctx)
	if err != nil {
		return recorded, err
	}

	know := func(subject, name string) {
		if _, known := recorded.names[subject]; known && (name == "" || recorded.names[subject] != "") {
			return
		}
		recorded.names[subject] = name
		if name != "" {
			key := strings.ToLower(name)
			recorded.byName[key] = append(recorded.byName[key], subject)
		}
	}
	for _, account := range accounts {
		know(account.Subject, account.Username)
	}
	for _, role := range roles {
		know(role.Subject, role.Username)
		grants := recorded.users[role.Subject]
		if role.ProjectID == nil {
			grants.Role = role.Role
			recorded.bootstrap[role.Subject] = role.GrantedBy == "bootstrap"
		} else {
			if grants.Projects == nil {
				grants.Projects = map[string]string{}
			}
			grants.Projects[role.ProjectSlug] = role.Role
		}
		recorded.users[role.Subject] = grants
	}
	for _, role := range groups {
		grants := recorded.groups[role.Group]
		if role.ProjectID == nil {
			grants.Role = role.Role
		} else {
			if grants.Projects == nil {
				grants.Projects = map[string]string{}
			}
			grants.Projects[role.ProjectSlug] = role.Role
		}
		recorded.groups[role.Group] = grants
	}
	return recorded, nil
}

// subjectOf resolves a declared account: its subject, or the one account carrying that name.
func (r recordedAccess) subjectOf(key string) (string, error) {
	if _, known := r.names[key]; known {
		return key, nil
	}
	subjects := r.byName[strings.ToLower(key)]
	switch len(subjects) {
	case 0:
		return "", errNeverSignedIn
	case 1:
		return subjects[0], nil
	}
	return "", fmt.Errorf("names %d accounts: declare it by its subject", len(subjects))
}

var errNeverSignedIn = errors.New("never signed in")

// label names an account in an operation, as the files do.
func (r recordedAccess) label(subject string) string {
	if name := r.names[subject]; name != "" && len(r.byName[strings.ToLower(name)]) == 1 {
		return name
	}
	return subject
}

// access plans the roles. After the projects: a grant may name a project the same files create.
// An instance without `access` leaves the roles as they are, prune or not: the screen grants them
// too, and a configuration that never declared them must not take them away.
func (p *planner) access(ctx context.Context, desired *configfile.Access, projects map[string]bool) {
	if desired == nil {
		return
	}
	recorded, err := readAccess(ctx, p.applier.Store)
	if err != nil {
		p.check(err)
		return
	}
	known := func(path, slug string) bool {
		if projects[slug] {
			return true
		}
		if _, err := p.applier.Store.ProjectBySlug(ctx, slug); err != nil {
			p.fail("%s: project %s is neither recorded nor declared", path, slug)
			return false
		}
		return true
	}
	actor := p.applier.Actor
	service := p.applier.Services.Access

	declaredUsers := map[string]configfile.Grants{}
	for _, name := range sortedKeys(desired.Users) {
		grants := desired.Users[name]
		subject, err := recorded.subjectOf(name)
		if errors.Is(err, errNeverSignedIn) {
			p.warn("access: %s has never signed in: granted once they have, or through a group", name)
			continue
		}
		if err != nil {
			p.fail("access: user %s %v", name, err)
			continue
		}
		declaredUsers[subject] = grants
		current := recorded.users[subject]
		username := recorded.names[subject]
		if grants.Role != "" && grants.Role != current.Role {
			p.add(fmt.Sprintf("~ access user %s: %s", name, grants.Role), func(ctx context.Context) error {
				return service.SetPerson(ctx, actor, subject, username, grants.Role, "")
			})
		}
		for _, slug := range sortedKeys(grants.Projects) {
			role := grants.Projects[slug]
			if role == current.Projects[slug] || !known("access: user "+name, slug) {
				continue
			}
			p.add(fmt.Sprintf("~ access user %s in %s: %s", name, slug, role), func(ctx context.Context) error {
				return service.SetPerson(ctx, actor, subject, username, role, slug)
			})
		}
	}

	for _, group := range sortedKeys(desired.Groups) {
		grants := desired.Groups[group]
		current := recorded.groups[group]
		if grants.Role != "" && grants.Role != current.Role {
			p.add(fmt.Sprintf("~ access group %s: %s", group, grants.Role), func(ctx context.Context) error {
				return service.SetGroup(ctx, actor, group, grants.Role, "")
			})
		}
		for _, slug := range sortedKeys(grants.Projects) {
			role := grants.Projects[slug]
			if role == current.Projects[slug] || !known("access: group "+group, slug) {
				continue
			}
			p.add(fmt.Sprintf("~ access group %s in %s: %s", group, slug, role), func(ctx context.Context) error {
				return service.SetGroup(ctx, actor, group, role, slug)
			})
		}
	}

	if !p.prune {
		return
	}
	for _, subject := range sortedKeys(recorded.users) {
		current, declared := recorded.users[subject], declaredUsers[subject]
		name := recorded.label(subject)
		if current.Role != "" && declared.Role == "" {
			switch {
			case current.Role == "admin":
				p.warn("access: %s stays administrator: the prune never removes one", name)
			case recorded.bootstrap[subject]:
				p.warn("access: %s keeps the role the start-up granted", name)
			default:
				p.add("- access user "+name, func(ctx context.Context) error {
					return service.ClearPerson(ctx, actor, subject, "")
				})
			}
		}
		for _, slug := range sortedKeys(current.Projects) {
			if _, kept := declared.Projects[slug]; !kept {
				p.add(fmt.Sprintf("- access user %s in %s", name, slug), func(ctx context.Context) error {
					return service.ClearPerson(ctx, actor, subject, slug)
				})
			}
		}
	}
	for _, group := range sortedKeys(recorded.groups) {
		current, declared := recorded.groups[group], desired.Groups[group]
		if current.Role != "" && declared.Role == "" {
			p.add("- access group "+group, func(ctx context.Context) error {
				return service.ClearGroup(ctx, actor, group, "")
			})
		}
		for _, slug := range sortedKeys(current.Projects) {
			if _, kept := declared.Projects[slug]; !kept {
				p.add(fmt.Sprintf("- access group %s in %s", group, slug), func(ctx context.Context) error {
					return service.ClearGroup(ctx, actor, group, slug)
				})
			}
		}
	}
}

// exportAccess writes the recorded roles in the files' shape: an account by its name when only
// it carries that name, by its subject otherwise.
func exportAccess(ctx context.Context, source AccessReadStore) (*configfile.Access, error) {
	recorded, err := readAccess(ctx, source)
	if err != nil {
		return nil, err
	}
	if len(recorded.users) == 0 && len(recorded.groups) == 0 {
		return nil, nil
	}
	access := &configfile.Access{}
	subjects := make([]string, 0, len(recorded.users))
	for subject := range recorded.users {
		subjects = append(subjects, subject)
	}
	sort.Strings(subjects)
	for _, subject := range subjects {
		if access.Users == nil {
			access.Users = map[string]configfile.Grants{}
		}
		access.Users[recorded.label(subject)] = recorded.users[subject]
	}
	if len(recorded.groups) > 0 {
		access.Groups = recorded.groups
	}
	return access, nil
}
