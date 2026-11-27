package configure

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Cadoles/arkhitekton/internal/auth"
	"github.com/Cadoles/arkhitekton/internal/configfile"
)

/*
 * Access in the versioned configuration: written when it changes, an account by its name, a
 * stranger left for later, and a prune that never removes an administrator.
 */

func signIn(t *testing.T, i instance, subject, username string) {
	t.Helper()
	if _, err := auth.NewSessionStore(i.db.Pool(), time.Hour).Create(context.Background(), subject, username, "", nil); err != nil {
		t.Fatal(err)
	}
}

func TestAccessIsAppliedOnceAndPrunedWithCare(t *testing.T) {
	i := configured(t)
	ctx := context.Background()
	signIn(t, i, "121", "vcarroy")
	signIn(t, i, "130", "alice")
	if err := i.services().Access.SetPerson(ctx, "admin", "121", "vcarroy", "admin", ""); err != nil {
		t.Fatal(err)
	}

	set := read(t, i.export(t))
	if set.Instance.Access == nil || set.Instance.Access.Users["vcarroy"].Role != "admin" {
		t.Fatalf("export = %+v", set.Instance.Access)
	}
	set.Instance.Access.Users["alice"] = configfile.Grants{Role: "operator", Projects: map[string]string{"portal": "viewer"}}
	set.Instance.Access.Users["ghost"] = configfile.Grants{Role: "viewer"}
	set.Instance.Access.Groups = map[string]configfile.Grants{"team:dev": {Role: "releaser"}}

	applier := i.applier(nil)
	ops, warnings := planOps(t, applier, set, false)
	want := "~ access user alice: operator\n~ access user alice in portal: viewer\n~ access group team:dev: releaser"
	if got := descriptions(ops); got != want {
		t.Fatalf("plan:\n%s", got)
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "ghost has never signed in") {
		t.Errorf("warnings = %v", warnings)
	}
	if err := applier.Execute(ctx, ops); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if again, _ := planOps(t, applier, set, false); len(again) != 0 {
		t.Errorf("after the change:\n%s", descriptions(again))
	}
	// The export reads back what was applied, and applies as it is.
	if again, _ := planOps(t, applier, read(t, i.export(t)), true); len(again) != 0 {
		t.Errorf("export then apply:\n%s", descriptions(again))
	}

	// Nobody declared: the others go, the administrator stays.
	set.Instance.Access = &configfile.Access{Groups: map[string]configfile.Grants{}}
	ops, warnings = planOps(t, applier, set, true)
	got := descriptions(ops)
	if !strings.Contains(got, "- access user alice\n- access user alice in portal") ||
		!strings.Contains(got, "- access group team:dev") || strings.Contains(got, "vcarroy") {
		t.Errorf("prune plan:\n%s", got)
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "vcarroy stays administrator") {
		t.Errorf("warnings = %v", warnings)
	}

	// An instance that never declared access leaves the roles alone, prune or not.
	set.Instance.Access = nil
	if ops, _ := planOps(t, applier, set, true); strings.Contains(descriptions(ops), "access") {
		t.Errorf("without access:\n%s", descriptions(ops))
	}
}

func TestAccessIsValidated(t *testing.T) {
	set := configfile.Set{Instance: &configfile.Instance{Access: &configfile.Access{
		Users:  map[string]configfile.Grants{"alice": {Role: "boss"}, "bob": {}},
		Groups: map[string]configfile.Grants{"team": {Projects: map[string]string{"portal": "admin"}}},
	}}}
	err := set.Validate()
	for _, expected := range []string{"users.alice.role", "users.bob: expected a role", "groups.team.projects.portal: admin"} {
		if err == nil || !strings.Contains(err.Error(), expected) {
			t.Errorf("%q missing from %v", expected, err)
		}
	}
}
