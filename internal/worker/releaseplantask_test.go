package worker

import (
	"context"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

type releasePlanStore struct {
	release store.Release
	moves   []string
}

func (s *releasePlanStore) GetReleaseByID(context.Context, int64) (store.Release, error) {
	return s.release, nil
}

func (s *releasePlanStore) GetProject(context.Context, int64) (store.Project, error) {
	return store.Project{ID: s.release.ProjectID, Slug: "app"}, nil
}

func (s *releasePlanStore) AdvanceReleaseState(_ context.Context, _ int64, from, to string) (bool, error) {
	s.moves = append(s.moves, from+"→"+to)
	return true, nil
}

func releasePlanTask(declared plan.Plan, ran *[]string) (*ReleasePlanTask, *releasePlanStore) {
	return releasePlanTaskWith(testAutomation{plan: declared}, ran)
}

func releasePlanTaskWith(automation testAutomation, ran *[]string) (*ReleasePlanTask, *releasePlanStore) {
	runner := stepRunnerFunc(func(_ context.Context, _ store.PlanRun, geste plan.Geste) (*int64, error) {
		*ran = append(*ran, geste.Ref)
		if geste.Kind == plan.GesteAction {
			return nil, nil
		}
		id := int64(len(*ran))
		return &id, nil
	})
	releases := &releasePlanStore{release: store.Release{ID: 7, ProjectID: 3, Tag: "1.0.0-rc1"}}
	planner := NewReleasePlanner(NewEngine(newFakePlanStore(), runner, quietLogger()), nil,
		automation, quietLogger())
	return NewReleasePlanTask(planner, releases, quietLogger()), releases
}

func planJob() store.Job {
	releaseID := int64(7)
	return store.Job{Kind: KindReleasePlan, ReleaseID: &releaseID, Params: map[string]string{"revision": "abc123"}}
}

// A release's plan opens in the worker, which carries out an action as readily as a pipeline: a
// plan starting with a commit status reaches its build.
func TestAReleasePlanStartingWithAnActionReachesItsBuild(t *testing.T) {
	var ran []string
	task, releases := releasePlanTask(plan.Plan{Stages: []plan.Stage{
		{Gestes: []plan.Geste{{Kind: plan.GesteAction, Ref: "gitea.status"}}},
		{Role: plan.RoleBuild, Gestes: []plan.Geste{{Kind: plan.GestePipeline, Ref: "docker/build", Label: "portal"}}},
	}}, &ran)

	if err := task.Run(context.Background(), planJob()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ran, ",") != "gitea.status,docker/build" {
		t.Errorf("ran %v", ran)
	}
	if len(releases.moves) != 0 {
		t.Errorf("moves %v: the release stays building", releases.moves)
	}
}

// A release from which nothing starts fails, and the job says why.
func TestAReleaseFromWhichNothingStartsFails(t *testing.T) {
	var ran []string
	task, releases := releasePlanTaskWith(testAutomation{plan: plan.Plan{Stages: []plan.Stage{
		{Role: plan.RoleBuild, Gestes: []plan.Geste{{Kind: plan.GestePipeline, Ref: "docker/build"}}},
	}}, off: true}, &ran)

	err := task.Run(context.Background(), planJob())
	if err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Errorf("error = %v: the job says why nothing started", err)
	}
	if strings.Join(releases.moves, ",") != "building→failed" {
		t.Errorf("moves %v", releases.moves)
	}
}

// A project that declares nothing to build or verify releases a tag: ready once launched.
func TestAProjectDeclaringNothingReleasesATag(t *testing.T) {
	var ran []string
	task, releases := releasePlanTask(plan.Plan{}, &ran)

	if err := task.Run(context.Background(), planJob()); err != nil {
		t.Errorf("error = %v", err)
	}
	if strings.Join(releases.moves, ",") != "building→ready" || len(ran) != 0 {
		t.Errorf("moves %v, ran %v", releases.moves, ran)
	}
}
