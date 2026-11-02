package configure

import (
	"context"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/configfile"
)

func TestTheExportWritesWhatWasConfigured(t *testing.T) {
	i := configured(t)
	exported, err := Exporter{Store: i.db, Registry: i.registry}.Export(context.Background())
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	set := exported.Set

	gitea := set.Instance.Plugins["gitea"]
	if gitea.Config["url"].Plain != "https://forge.example" {
		t.Errorf("gitea url = %+v", gitea.Config["url"])
	}
	if token := gitea.Config["token"]; token.Env != "ARK_GITEA_TOKEN" || token.Plain != "" {
		t.Errorf("token = %+v, want a reference to ARK_GITEA_TOKEN", token)
	}
	if set.Instance.QueueLimits["php/static-tests"] != 2 {
		t.Errorf("queue limits = %v", set.Instance.QueueLimits)
	}

	project, ok := set.Project("portal")
	if !ok {
		t.Fatalf("project portal missing: %+v", set.Projects)
	}
	if project.Repository != "team/portal" || len(project.ReleaseKinds) != 1 || project.BuildContext != "" {
		t.Errorf("project = %+v", project)
	}
	if len(project.Targets) != 2 || !configfile.IsOn(project.Targets[0].Promote) || configfile.IsOn(project.Targets[1].Promote) {
		t.Errorf("targets = %+v", project.Targets)
	}
	if secret := project.Plugins["gitea"].Config["webhook_secret"]; secret.Env != "ARK_PORTAL_GITEA_WEBHOOK_SECRET" {
		t.Errorf("project secret = %+v", secret)
	}
	if enabled, declared := project.Pipelines["php/static-tests"]; !declared || enabled {
		t.Errorf("pipelines = %v", project.Pipelines)
	}
	if project.TrackerStatuses["En recette"] != "client-test" || len(project.TrackerStatuses) != 1 {
		t.Errorf("statuses = %v, want the project's own mapping only", project.TrackerStatuses)
	}
	if project.ReleaseAutomation == nil || len(project.ReleaseAutomation.Stages) != 1 {
		t.Errorf("automation = %+v", project.ReleaseAutomation)
	}
	if len(project.Reactions) != 1 || project.Reactions[0].Event != "gitea.push" {
		t.Errorf("reactions = %+v", project.Reactions)
	}
	if len(project.Schedules) != 1 || configfile.IsOn(project.Schedules[0].Enabled) {
		t.Errorf("schedules = %+v", project.Schedules)
	}

	want := []string{"ARK_GITEA_TOKEN", "ARK_PORTAL_GITEA_WEBHOOK_SECRET"}
	if strings.Join(exported.Secrets, ",") != strings.Join(want, ",") {
		t.Errorf("secrets = %v, want %v", exported.Secrets, want)
	}
}

// No sensitive value leaves the database through an export, in clear or sealed, and nothing the
// dashboard observed is written.
func TestTheExportCarriesNoSecretAndNoObservedState(t *testing.T) {
	i := configured(t)
	ctx := context.Background()
	project, _ := i.db.ProjectBySlug(ctx, "portal")
	if err := i.db.RecordProjectCheck(ctx, project.ID, true, "default branch: main"); err != nil {
		t.Fatalf("check: %v", err)
	}

	exported, err := Exporter{Store: i.db, Registry: i.registry}.Export(ctx)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	raw, err := configfile.Encode(exported.Set)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	text := string(raw)
	for _, leaked := range []string{typedToken, "per-project-secret", "v1:", "default branch", "next_fire", "checked"} {
		if strings.Contains(text, leaked) {
			t.Errorf("the export carries %q:\n%s", leaked, text)
		}
	}

	again, err := Exporter{Store: i.db, Registry: i.registry}.Export(ctx)
	if err != nil {
		t.Fatalf("second export: %v", err)
	}
	second, _ := configfile.Encode(again.Set)
	if string(second) != text {
		t.Errorf("two exports differ:\n%s\n---\n%s", text, second)
	}
}
