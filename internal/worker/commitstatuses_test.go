package worker

import (
	"context"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

type statusWriter struct{ set []string }

func (w *statusWriter) CreateTag(context.Context, string, string, string) error { return nil }

func (w *statusWriter) SetCommitStatus(_ context.Context, commit, state, name, description, link string) error {
	w.set = append(w.set, strings.Join([]string{commit, state, name, description, link}, " "))
	return nil
}

type statusForges struct{ writer provider.TagWriter }

func (f statusForges) TagWriter(context.Context, int64) (provider.TagWriter, error) {
	return f.writer, nil
}

type statusProjects struct{}

func (statusProjects) GetProject(context.Context, int64) (store.Project, error) {
	return store.Project{Slug: "mse"}, nil
}

func statusRun(statuses bool) store.PlanRun {
	projectID, releaseID := int64(3), int64(7)
	return store.PlanRun{
		ID: 1, ProjectID: &projectID, ReleaseID: &releaseID,
		Plan:    plan.Plan{Statuses: statuses},
		Context: map[string]string{"revision": "abc123", "tag": "1.0.0-rc1"},
	}
}

// A plan that asks for statuses reports each pipeline step on its commit, with a link to the
// release in its project.
func TestAStepIsReportedOnItsCommit(t *testing.T) {
	writer := &statusWriter{}
	statuses := NewCommitStatuses(statusForges{writer}, statusProjects{}, "https://dash.example/", quietLogger())
	build := plan.Geste{Kind: plan.GestePipeline, Ref: "docker/build", Label: "portal"}

	statuses.StepChanged(context.Background(), statusRun(true), build, plan.Running)
	statuses.StepChanged(context.Background(), statusRun(true), build, plan.Failed)

	expected := []string{
		"abc123 pending arkhitekton/portal En cours https://dash.example/releases/1.0.0-rc1?project=mse",
		"abc123 failure arkhitekton/portal Échoué https://dash.example/releases/1.0.0-rc1?project=mse",
	}
	if strings.Join(writer.set, "\n") != strings.Join(expected, "\n") {
		t.Errorf("statuses:\n%s", strings.Join(writer.set, "\n"))
	}
}

// A plan that does not ask, or a run with no commit, reports nothing.
func TestAStepIsReportedOnlyWhenThePlanAsks(t *testing.T) {
	writer := &statusWriter{}
	statuses := NewCommitStatuses(statusForges{writer}, statusProjects{}, "https://dash.example", quietLogger())
	build := plan.Geste{Kind: plan.GestePipeline, Ref: "docker/build"}

	statuses.StepChanged(context.Background(), statusRun(false), build, plan.Running)
	noCommit := statusRun(true)
	noCommit.Context = map[string]string{}
	statuses.StepChanged(context.Background(), noCommit, build, plan.Running)

	if len(writer.set) != 0 {
		t.Errorf("statuses set: %v", writer.set)
	}
}

// The engine tells the sink when a pipeline step starts and when it concludes.
func TestTheEngineTellsEachPipelineStep(t *testing.T) {
	base := newFakePlanStore()
	runner := stepRunnerFunc(func(context.Context, store.PlanRun, plan.Geste) (*int64, error) {
		id := int64(5)
		return &id, nil
	})
	sink := &recordingSteps{}
	engine := NewEngine(base, runner, quietLogger()).WithSteps(sink)

	engine.Trigger(context.Background(), store.PlanRun{Trigger: "trial", EventKey: "trial:1", Plan: plan.Plan{
		Stages: []plan.Stage{{Gestes: []plan.Geste{{Kind: plan.GestePipeline, Ref: "docker/build", Label: "portal"}}}},
	}})
	engine.Advance(context.Background(), 5, plan.Succeeded)

	if strings.Join(sink.seen, ",") != "portal:running,portal:succeeded" {
		t.Errorf("seen %v", sink.seen)
	}
}

type recordingSteps struct{ seen []string }

func (r *recordingSteps) StepChanged(_ context.Context, _ store.PlanRun, geste plan.Geste, state plan.State) {
	r.seen = append(r.seen, geste.Label+":"+string(state))
}
