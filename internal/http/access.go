package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
)

/*
 * Who may use the dashboard, as the administration reads it: one entry per person, with what they
 * may do and where it comes from, and the roles given to the identity provider's groups.
 *
 * A person's access is resolved here by the same rule as at each request (auth.Resolve): their
 * own role wins at a scope, failing one the highest of their groups', then a project's role wins
 * over the instance's.
 */

type grantPayload struct {
	Project      string    `json:"project,omitempty"`
	ProjectLabel string    `json:"project_label,omitempty"`
	Role         string    `json:"role"`
	GrantedBy    string    `json:"granted_by"`
	GrantedAt    time.Time `json:"granted_at"`
	// Bootstrap is a role the start-up configuration granted: removed, it does not come back,
	// but saying where it came from avoids believing a stranger granted it.
	Bootstrap bool `json:"bootstrap,omitempty"`
}

type effectivePayload struct {
	Project      string `json:"project,omitempty"`
	ProjectLabel string `json:"project_label,omitempty"`
	// Role is empty when nothing applies at this scope.
	Role string `json:"role"`
	// Source is the group the role comes from, empty when it is the person's own.
	Source string `json:"source,omitempty"`
}

type personPayload struct {
	Subject  string     `json:"subject"`
	Username string     `json:"username,omitempty"`
	Email    string     `json:"email,omitempty"`
	Groups   []string   `json:"groups"`
	LastSeen *time.Time `json:"last_seen,omitempty"`
	Sessions int        `json:"sessions"`
	// Roles are the person's own grants.
	Roles []grantPayload `json:"roles"`
	// Global is what applies across the instance; Projects, where a project differs.
	Global   effectivePayload   `json:"global"`
	Projects []effectivePayload `json:"projects"`
	// Pending is a person who signed in and may do nothing yet.
	Pending bool `json:"pending"`
}

// projectNames names the projects grants point at, by identifier.
type projectNames map[int64][2]string

func (names projectNames) add(id *int64, slug, label string) {
	if id != nil {
		names[*id] = [2]string{slug, label}
	}
}

// People returns everyone who signed in or holds a role, those waiting for access first.
func (h *AdminHandler) People(w http.ResponseWriter, r *http.Request) {
	accounts, err := h.store.ListAccounts(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the accounts", "")
		return
	}
	roles, err := h.store.ListUserRoles(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the roles", "")
		return
	}
	groupRoles, err := h.store.ListGroupRoles(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the group roles", "")
		return
	}
	who := resolveActors(r.Context(), h.store, slog.Default())

	names := projectNames{}
	byGroup := map[string][]auth.Grant{}
	for _, grant := range groupRoles {
		names.add(grant.ProjectID, grant.ProjectSlug, grant.ProjectLabel)
		byGroup[grant.Group] = append(byGroup[grant.Group],
			auth.Grant{ProjectID: grant.ProjectID, Role: auth.Role(grant.Role), Group: grant.Group})
	}

	people := map[string]*personPayload{}
	personal := map[string][]auth.Grant{}
	var order []string
	person := func(subject string) *personPayload {
		if entry, found := people[subject]; found {
			return entry
		}
		entry := &personPayload{Subject: subject, Groups: []string{}, Roles: []grantPayload{}}
		people[subject] = entry
		order = append(order, subject)
		return entry
	}
	for _, account := range accounts {
		entry := person(account.Subject)
		lastSeen := account.LastSeen
		entry.Username, entry.Email, entry.LastSeen, entry.Sessions =
			account.Username, account.Email, &lastSeen, account.Sessions
		if account.Groups != nil {
			entry.Groups = account.Groups
		}
	}
	for _, role := range roles {
		entry := person(role.Subject)
		if entry.Username == "" {
			entry.Username = role.Username
		}
		entry.Sessions = max(entry.Sessions, role.Sessions)
		names.add(role.ProjectID, role.ProjectSlug, role.ProjectLabel)
		entry.Roles = append(entry.Roles, grantPayload{
			Project: role.ProjectSlug, ProjectLabel: role.ProjectLabel, Role: role.Role,
			GrantedBy: who.name(role.GrantedBy), GrantedAt: role.GrantedAt,
			Bootstrap: role.GrantedBy == "bootstrap",
		})
		personal[role.Subject] = append(personal[role.Subject],
			auth.Grant{ProjectID: role.ProjectID, Role: auth.Role(role.Role)})
	}

	payload := make([]personPayload, 0, len(order))
	for _, subject := range order {
		entry := people[subject]
		var fromGroups []auth.Grant
		for _, group := range entry.Groups {
			fromGroups = append(fromGroups, byGroup[group]...)
		}
		access := auth.Resolve(personal[subject], fromGroups)
		entry.Global = effectivePayload{Role: string(access.Global.Role), Source: access.Global.Source}
		entry.Projects = []effectivePayload{}
		for id, effective := range access.Projects {
			entry.Projects = append(entry.Projects, effectivePayload{
				Project: names[id][0], ProjectLabel: names[id][1],
				Role: string(effective.Role), Source: effective.Source,
			})
		}
		sort.Slice(entry.Projects, func(i, j int) bool { return entry.Projects[i].Project < entry.Projects[j].Project })
		entry.Pending = access.None()
		payload = append(payload, *entry)
	}
	// Those waiting first: they are why one opens the screen.
	sort.SliceStable(payload, func(i, j int) bool { return payload[i].Pending && !payload[j].Pending })

	writeJSON(w, http.StatusOK, map[string]any{"people": payload})
}

type groupPayload struct {
	Group string `json:"group"`
	// Members is the number of accounts the provider last put in the group.
	Members int            `json:"members"`
	Roles   []grantPayload `json:"roles"`
}

// Groups returns the roles given to groups, and the groups seen in accounts, which can be given
// one.
func (h *AdminHandler) Groups(w http.ResponseWriter, r *http.Request) {
	accounts, err := h.store.ListAccounts(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the accounts", "")
		return
	}
	roles, err := h.store.ListGroupRoles(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the group roles", "")
		return
	}
	who := resolveActors(r.Context(), h.store, slog.Default())

	members := map[string]int{}
	for _, account := range accounts {
		for _, group := range account.Groups {
			members[group]++
		}
	}

	granted := []groupPayload{}
	index := map[string]int{}
	for _, role := range roles {
		position, found := index[role.Group]
		if !found {
			position = len(granted)
			index[role.Group] = position
			granted = append(granted, groupPayload{Group: role.Group, Members: members[role.Group], Roles: []grantPayload{}})
		}
		granted[position].Roles = append(granted[position].Roles, grantPayload{
			Project: role.ProjectSlug, ProjectLabel: role.ProjectLabel, Role: role.Role,
			GrantedBy: who.name(role.GrantedBy), GrantedAt: role.GrantedAt,
		})
	}

	known := make([]groupPayload, 0, len(members))
	for group, count := range members {
		known = append(known, groupPayload{Group: group, Members: count, Roles: []grantPayload{}})
	}
	sort.Slice(known, func(i, j int) bool { return known[i].Group < known[j].Group })

	writeJSON(w, http.StatusOK, map[string]any{"groups": granted, "known": known})
}

// SetGroupRole gives a group a role, everywhere or in one project. It applies to the members'
// next request.
func (h *AdminHandler) SetGroupRole(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}
	var request struct {
		Role    string `json:"role"`
		Project string `json:"project,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request", "")
		return
	}
	group := strings.TrimSpace(r.PathValue("group"))
	if err := h.access.SetGroup(r.Context(), identity.Subject, group, request.Role, request.Project); err != nil {
		writeRefusal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"group": group, "role": request.Role})
}

// ClearGroupRole removes a group's role at one scope.
func (h *AdminHandler) ClearGroupRole(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}
	group := strings.TrimSpace(r.PathValue("group"))
	if err := h.access.ClearGroup(r.Context(), identity.Subject, group, r.URL.Query().Get("project")); err != nil {
		writeRefusal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"group": group})
}
