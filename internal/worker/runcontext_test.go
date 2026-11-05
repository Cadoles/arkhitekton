package worker

import (
	"context"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/reaction"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

type contextStore struct {
	release   store.Release
	notes     []store.NoteEntry
	artifacts []store.PlanArtifact
	reactions []reaction.Reaction
}

func (s contextStore) GetProject(context.Context, int64) (store.Project, error) {
	return store.Project{Slug: "mse", VersionScheme: "semver", Dockerfile: "./misc/Dockerfile", BuildContext: "."}, nil
}

func (s contextStore) GetReleaseByID(_ context.Context, id int64) (store.Release, error) {
	if id != s.release.ID {
		return store.Release{}, store.ErrReleaseNotFound
	}
	return s.release, nil
}

func (s contextStore) ListNoteEntries(context.Context, int64) ([]store.NoteEntry, error) {
	return s.notes, nil
}

func (s contextStore) ReleaseArtifacts(context.Context, int64) ([]store.PlanArtifact, error) {
	return s.artifacts, nil
}

func (s contextStore) ReactionsFor(context.Context, int64, string) ([]reaction.Reaction, error) {
	return s.reactions, nil
}

// A run on a release reads its link, its issues, its images and whether it is a pre-release.
func TestARunReadsItsReleasesVariables(t *testing.T) {
	source := contextStore{
		release: store.Release{ID: 7, ProjectID: 3, Tag: "1.0.0-rc1"},
		notes: []store.NoteEntry{
			{ExternalID: "26832", Subject: "Correction frankenphp"},
			{ExternalID: "9999"},
		},
		artifacts: []store.PlanArtifact{
			{Name: "image", Reference: "reg/portal:1.0.0-rc1"},
			{Name: "tag", Reference: "1.0.0-rc1"},
			{Name: "image", Reference: "reg/portal-dev:1.0.0-rc1"},
		},
	}
	issues := func(_ context.Context, _ int64, issue string) string {
		return "https://tracker/issues/" + issue
	}
	releaseID, projectID := int64(7), int64(3)
	got := NewRunContext(source, "https://dash", issues, quietLogger()).
		Enrich(context.Background(), store.PlanRun{ReleaseID: &releaseID, ProjectID: &projectID})

	if got[contextDockerfile] != "./misc/Dockerfile" || got[contextBuildContext] != "." {
		t.Errorf("project variables %q %q", got[contextDockerfile], got[contextBuildContext])
	}
	if got[contextReleaseURL] != "https://dash/releases/1.0.0-rc1?project=mse" {
		t.Errorf("url %q", got[contextReleaseURL])
	}
	if got[contextPrerelease] != "true" {
		t.Errorf("prerelease %q", got[contextPrerelease])
	}
	expected := "- [#26832](https://tracker/issues/26832) Correction frankenphp\n- [#9999](https://tracker/issues/9999)"
	if got[contextReleaseNotes] != expected {
		t.Errorf("notes:\n%s", got[contextReleaseNotes])
	}
	if got[contextReleaseImages] != "- `reg/portal:1.0.0-rc1`\n- `reg/portal-dev:1.0.0-rc1`" {
		t.Errorf("images:\n%s", got[contextReleaseImages])
	}
}

// The summary lists a plan's pipelines and their state; each step's state is a variable too.
func TestAPlansSummaryListsItsPipelines(t *testing.T) {
	progress := plan.Start(plan.Plan{Stages: []plan.Stage{
		{Gestes: []plan.Geste{{Kind: plan.GestePipeline, Ref: "docker/build", Label: "portal-dev"}}},
		{Gestes: []plan.Geste{
			{Kind: plan.GestePipeline, Ref: "php/unit-tests"},
			{Kind: plan.GesteAction, Ref: "gitea.comment"},
		}},
	}})
	progress, _ = progress.Mark(plan.Position{Stage: 0, Geste: 0}, plan.Succeeded)
	progress, _ = progress.Mark(plan.Position{Stage: 1, Geste: 0}, plan.Failed)

	variables := stepVariables(progress)
	if variables["steps.portal-dev.state"] != "succeeded" || variables["steps.php/unit-tests.state"] != "failed" {
		t.Errorf("states %v", variables)
	}
	summary := variables[contextPlanSummary]
	if !strings.Contains(summary, "| portal-dev | ✅ réussi |") ||
		!strings.Contains(summary, "| php/unit-tests | ❌ échoué |") || strings.Contains(summary, "gitea.comment") {
		t.Errorf("summary:\n%s", summary)
	}
}

// A fact naming a release of its project opens its plan on that release; one naming another
// project's release opens it on none.
func TestAFactsRunBearsOnTheReleaseItNames(t *testing.T) {
	for _, tc := range []struct {
		project int64
		linked  bool
	}{{3, true}, {4, false}} {
		base := newFakePlanStore()
		engine := NewEngine(base, stepRunnerFunc(func(context.Context, store.PlanRun, plan.Geste) (*int64, error) {
			return nil, nil
		}), quietLogger())
		source := contextStore{
			release: store.Release{ID: 7, ProjectID: 3, Tag: "1.0.0"},
			reactions: []reaction.Reaction{{Name: "notify", EventType: "mse.release.promoted",
				Plan: plan.Plan{Stages: []plan.Stage{{Gestes: []plan.Geste{{Kind: plan.GesteAction, Ref: "rocketchat.message"}}}}}}},
		}
		projectID := tc.project
		NewDispatcher(source, engine, quietLogger()).Dispatch(context.Background(), event.Event{
			Type: "mse.release.promoted", ProjectID: &projectID, DedupKey: "k",
			Payload: map[string]string{"release_id": "7", "environment": "unstable"},
		})
		run, err := base.PlanRunByID(context.Background(), 1)
		if err != nil {
			t.Fatal(err)
		}
		if (run.ReleaseID != nil) != tc.linked {
			t.Errorf("project %d: release %v", tc.project, run.ReleaseID)
		}
	}
}

// A release with no issue has empty notes, not a variable a template leaves literal.
func TestAReleaseWithNoIssueHasEmptyNotes(t *testing.T) {
	releaseID := int64(7)
	got := NewRunContext(contextStore{release: store.Release{ID: 7, ProjectID: 3, Tag: "1.0.0"}}, "", nil, quietLogger()).
		Enrich(context.Background(), store.PlanRun{ReleaseID: &releaseID})
	if notes, present := got[contextReleaseNotes]; !present || notes != "" {
		t.Errorf("notes %q, present %v", notes, present)
	}
}
