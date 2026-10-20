package worker

import (
	"context"
	"errors"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The catch-up, and the only property that counts: it breaks nothing on its way.
 *
 * Without it, every project would stop building on the deployment of this version.
 */

// catchUpStore keeps what the catch-up wrote.
type catchUpStore struct {
	without         []store.Project
	written         map[int64]plan.Plan
	actorsByName    map[int64]string
	switchedOffList map[string]bool
	err             error
}

func newCatchUpStore(projects ...store.Project) *catchUpStore {
	return &catchUpStore{
		without: projects, written: map[int64]plan.Plan{},
		actorsByName: map[int64]string{}, switchedOffList: map[string]bool{},
	}
}

func (r *catchUpStore) ProjectsWithoutAutomation(context.Context) ([]store.Project, error) {
	return r.without, nil
}

func (r *catchUpStore) SaveReleaseAutomation(
	_ context.Context, entry store.ReleaseAutomation, actor string,
) error {
	if r.err != nil {
		return r.err
	}
	r.written[entry.ProjectID] = entry.Plan
	r.actorsByName[entry.ProjectID] = actor
	return nil
}

func (r *catchUpStore) PipelineEnabled(_ context.Context, _ int64, kind string) (bool, error) {
	return !r.switchedOffList[kind], nil
}

func catchUp(t *testing.T, db *catchUpStore) {
	t.Helper()
	if err := NewAutomationCatchUp(db, testCatalogue(t), quietLogger()).
		Run(context.Background()); err != nil {
		t.Fatalf("catch-up: %v", err)
	}
}

// The catch-up returns what ran the day before.
//
// That is its whole contract: the plan caught up is the one the code composed, by
// construction and not by re-reading.
func TestTheCatchUpReturnsWhatRanTheDayBefore(t *testing.T) {
	db := newCatchUpStore(twoImageProject())
	catchUp(t, db)

	caught, present := db.written[1]
	if !present {
		t.Fatal("the project was not caught up")
	}
	expected := DefaultAutomation(testCatalogue(t), twoImageProject(), nil)
	if len(caught.Stages) != len(expected.Stages) {
		t.Fatalf("%d stage(s), expected %d", len(caught.Stages), len(expected.Stages))
	}
	if caught.Stages[0].Role != plan.RoleBuild || caught.Stages[1].Role != plan.RoleTest {
		t.Errorf("roles = %q / %q", caught.Stages[0].Role, caught.Stages[1].Role)
	}
	if len(caught.Stages[0].Gestes) != 2 {
		t.Errorf("%d build(s), expected one per target", len(caught.Stages[0].Gestes))
	}
	// The author is readable on screen: someone opening the tab must be able to say "this was not
	// typed, it comes from my targets".
	if db.actorsByName[1] != catchUpActor {
		t.Errorf("author = %q", db.actorsByName[1])
	}
}

// A pipeline targets a build's artifact, when the project performs it.
//
// That is what ties the two stages other than by the accident of a recomposed reference: the
// audit analyses the image the build has just published.
func TestTheCatchUpTargetsTheArtifactWhenTheProjectBuildsTheImage(t *testing.T) {
	db := newCatchUpStore(twoImageProject())
	catchUp(t, db)

	audit := stepNamed(t, db.written[1], "security-audit")
	if audit.Params["portal-image"] != "{artifacts.portal.image}" {
		t.Errorf("parameter = %q, expected the build's artifact",
			audit.Params["portal-image"])
	}
}

// A project that does not build that image keeps the fallback, and goes on running.
//
// Targeting an artifact nobody produces would fail the step, naming it — which is right, but
// a catch-up must break nothing on its way. The fallback recomposes the reference from the
// registry, as the day before.
func TestTheCatchUpDoesNotTargetAnArtifactTheProjectDoesNotBuild(t *testing.T) {
	other := twoImageProject()
	other.Targets = []store.Target{{Stage: "app", Image: "autre-chose"}}

	db := newCatchUpStore(other)
	catchUp(t, db)

	audit := stepNamed(t, db.written[1], "security-audit")
	if _, present := audit.Params["portal-image"]; present {
		t.Errorf("artifact %q is targeted although the project does not build it",
			audit.Params["portal-image"])
	}
}

// A kind switched off on the project does not come back through the catch-up.
func TestTheCatchUpDoesNotSwitchBackOnASwitchedOffKind(t *testing.T) {
	db := newCatchUpStore(twoImageProject())
	db.switchedOffList["e2e-tests"] = true
	catchUp(t, db)

	for _, geste := range db.written[1].Stages[1].Gestes {
		if geste.Ref == "e2e-tests" {
			t.Error("a switched-off kind was caught up")
		}
	}
}

// A project that composes nothing still gets its row.
//
// Without it, the catch-up would re-examine it at every start-up. And the row is true: it
// built nothing the day before either.
func TestAProjectWithNothingToComposeStillGetsItsRow(t *testing.T) {
	empty := store.Project{ID: 2, Slug: "empty"}
	db := newCatchUpStore(empty)
	db.switchedOffList["security-audit"] = true
	db.switchedOffList["unit-tests"] = true
	db.switchedOffList["e2e-tests"] = true

	catchUp(t, db)

	caught, present := db.written[2]
	if !present {
		t.Fatal("the project has no row — it would be re-examined at every start-up")
	}
	if len(caught.Stages) != 0 {
		t.Errorf("plan = %+v, expected empty", caught)
	}
}

// A project that could not be caught up does not stop the next ones.
//
// It will be seen again at the next start-up, since it still has no row. Interrupting the
// pass would deprive all the others of their automation.
func TestAProjectNotCaughtUpDoesNotStopTheNextOnes(t *testing.T) {
	db := newCatchUpStore(twoImageProject(), store.Project{ID: 2, Slug: "second"})
	db.err = errors.New("write refused")

	// The error is logged and not returned: the catch-up goes through to the end.
	if err := NewAutomationCatchUp(db, testCatalogue(t), quietLogger()).
		Run(context.Background()); err != nil {
		t.Fatalf("the catch-up was interrupted: %v", err)
	}
}

// stepNamed returns the step of a plan carrying this reference.
func stepNamed(t *testing.T, subject plan.Plan, ref string) plan.Geste {
	t.Helper()
	for _, stage := range subject.Stages {
		for _, geste := range stage.Gestes {
			if geste.Ref == ref {
				return geste
			}
		}
	}
	t.Fatalf("no step %q in %+v", ref, subject)
	return plan.Geste{}
}
