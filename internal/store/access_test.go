package store

import (
	"context"
	"errors"
	"testing"
)

func TestAccountsAreListedAndNamed(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	if _, err := db.pool.Exec(ctx,
		`insert into accounts (subject, username, email, groups, last_seen)
		 values ('121', 'vcarroy', 'v@example.fr', '{cnous,cnous:dev}', now()),
		        ('130', 'alice', null, '{}', now() - interval '1 day')`); err != nil {
		t.Fatal(err)
	}
	// A role granted under another name: the name signed in with wins.
	if err := db.SetRoleIn(ctx, "121", "typed", "admin", nil, "test"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetRoleIn(ctx, "140", "bob", "viewer", nil, "test"); err != nil {
		t.Fatal(err)
	}

	accounts, err := db.ListAccounts(ctx)
	if err != nil || len(accounts) != 2 || accounts[0].Subject != "121" || len(accounts[0].Groups) != 2 ||
		accounts[1].Email != "" {
		t.Fatalf("accounts = %+v (%v)", accounts, err)
	}
	names, err := db.Directory(ctx)
	if err != nil || names["121"] != "vcarroy" || names["130"] != "alice" || names["140"] != "bob" {
		t.Errorf("directory = %v (%v)", names, err)
	}
}

func TestAGroupHasOneRoleAtEachScope(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	project := int64(testProjectID)

	for _, step := range []struct {
		role    string
		project *int64
	}{{"viewer", nil}, {"operator", nil}, {"releaser", &project}} {
		if err := db.SetGroupRoleIn(ctx, "cnous:dev", step.role, step.project, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetGroupRoleIn(ctx, "cnous:dev", "admin", &project, "test"); err == nil {
		t.Error("admin was granted in a project")
	}

	roles, err := db.ListGroupRoles(ctx)
	if err != nil || len(roles) != 2 || roles[0].Role != "operator" || roles[0].ProjectID != nil ||
		roles[1].Role != "releaser" || roles[1].ProjectSlug != "test" {
		t.Fatalf("roles = %+v (%v)", roles, err)
	}

	if err := db.ClearGroupRoleIn(ctx, "cnous:dev", &project); err != nil {
		t.Fatal(err)
	}
	if err := db.ClearGroupRoleIn(ctx, "cnous:dev", &project); !errors.Is(err, ErrRoleNotFound) {
		t.Errorf("clearing twice: %v", err)
	}
	if roles, _ := db.ListGroupRoles(ctx); len(roles) != 1 {
		t.Errorf("after clearing = %+v", roles)
	}
}
