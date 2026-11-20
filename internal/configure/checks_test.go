package configure

import (
	"context"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

type fixedProber struct{ keysURL string }

func (p fixedProber) Probe(_ context.Context, _, _ string) (provider.Probe, error) {
	return provider.Probe{DefaultBranch: "main", KeysURL: p.keysURL, KeyNote: "no key of its own yet"}, nil
}

// Every project is checked; a second round that finds the same records nothing in the audit log.
func TestEveryProjectIsCheckedAndOnlyChangesAreAudited(t *testing.T) {
	i := configured(t)
	ctx := context.Background()
	checks := Checks{Store: i.db, Prober: fixedProber{keysURL: "https://forge/team/portal/settings/keys"}, Logger: quiet()}

	audited := func() int {
		entries, err := i.db.ListJournal(ctx, store.JournalQuery{Nature: store.JournalAction, Search: store.AuditProjectProbe})
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	if err := checks.CheckAll(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	first := audited()
	project, _ := i.db.ProjectBySlug(ctx, "portal")
	if project.CheckOK == nil || !*project.CheckOK || project.KeysURL != "https://forge/team/portal/settings/keys" ||
		project.KeyNote == "" || first == 0 {
		t.Fatalf("project %+v, audited %d", project, first)
	}

	if err := checks.CheckAll(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	if audited() != first {
		t.Errorf("a round that changed nothing was audited")
	}
}
