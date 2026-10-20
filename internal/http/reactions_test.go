package http

import (
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The catalogue of the core's facts, and the second truth it constitutes.
 *
 * `coreEventChoices` is written by hand: the core's facts have no descriptor, and giving them one
 * for the sole convenience of this list would add a registry to keep aligned. The price of that
 * choice is that the list may drift from what the constructors produce — and the drift is
 * **mute**: the editor would offer a field no payload carries, and the matching would never
 * match.
 *
 * These two tests are the counterweight to that choice.
 */

// instant fixes the date of the example facts.
var instant = time.Date(2026, 9, 2, 14, 30, 0, 0, time.UTC)

// The catalogue offers exactly the three core facts.
//
// The reduction removed three of them. Leaving them here would have left them choosable in
// the editor, for reactions nothing triggers any more.
func TestTheCatalogueOffersTheCoresThreeFacts(t *testing.T) {
	expectedItems := map[string]bool{
		string(event.VersionCreated): true, string(event.ReleaseCreated): true,
		string(event.ReleaseReleased): true,
	}

	for _, choice := range coreEventChoices() {
		if !expectedItems[choice.Type] {
			t.Errorf("fact %q offered — it is not one of the core's three", choice.Type)
		}
		delete(expectedItems, choice.Type)
		if choice.Label == "" {
			t.Errorf("fact %q with no label — the editor's list would be mute", choice.Type)
		}
	}
	for missing := range expectedItems {
		t.Errorf("fact %q is not offered to the editor", missing)
	}
}

// Every announced field exists in the payload the constructor produces.
//
// That is the check that counts: a misspelled field here would be offered in the editor,
// saved without complaint, and the match would never match. Nothing would say so — a
// reaction that does not leave looks like a reaction nobody triggered.
func TestEveryAnnouncedFieldExistsInThePayload(t *testing.T) {
	dueDate := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	// The constructors filled in completely: an optional field left bare would make its
	// absence look like a typo.
	charges := map[string]map[string]string{
		string(event.VersionCreated): event.VersionCreatedAt(
			instant, 805, "v3.2.0", "open", "redmine", "412", &dueDate).Payload,
		string(event.ReleaseCreated): event.ReleaseCreatedAt(
			instant, 7, 42, "1.8.0-rc1", "pre", "main", "1.8.0").Payload,
		string(event.ReleaseReleased): event.ReleaseReleasedAt(
			instant, 7, 42, "1.8.0", "vcarroy").Payload,
	}

	for _, choice := range coreEventChoices() {
		charge, known := charges[choice.Type]
		if !known {
			t.Errorf("fact %q with no sample — the check does not cover it", choice.Type)
			continue
		}
		for _, field := range choice.Fields {
			if _, present := charge[field]; !present {
				t.Errorf("fact %q: field %q is offered and the payload does not carry it",
					choice.Type, field)
			}
		}
		// And the reverse: a field carried but not announced is matchable without being offered.
		// Less serious — nothing breaks — but it is a capability offered without saying so.
		for field := range charge {
			if !slices.Contains(choice.Fields, field) {
				t.Errorf("fact %q: the payload carries %q and the editor does not offer it",
					choice.Type, field)
			}
		}
	}
}

/*
 * A stage's role, checked and not taken on trust.
 *
 * Without this check, a release's life cycle would change by accident: adding a notification to
 * the build stage would make the move into verification depend on a webhook.
 */

// roleCatalogue returns a catalogue carrying a build and a test.
func roleCatalogue() *catalog.Catalog {
	return &catalog.Catalog{Pipelines: []catalog.Pipeline{
		{Kind: "build", PipelineRef: "build-image",
			Produces: []catalog.Product{catalog.ProductImage}},
		{Kind: "unit-tests", PipelineRef: "unit", Verification: true,
			Produces: []catalog.Product{catalog.ProductResults}},
	}}
}

func planWithStage(role plan.Role, steps ...plan.Geste) plan.Plan {
	return plan.Plan{Stages: []plan.Stage{{Role: role, Gestes: steps}}}
}

func pipelineGeste(ref string) plan.Geste {
	return plan.Geste{Kind: plan.GestePipeline, Ref: ref}
}

// A build stage whose step produces no image is refused.
func TestABuildStageRequiresAStepThatProducesAnImage(t *testing.T) {
	handler := &AutomationsHandler{catalog: roleCatalogue()}

	err := handler.checkRoles(planWithStage(plan.RoleBuild, pipelineGeste("unit-tests")))
	if err == nil {
		t.Fatal("a test pipeline was admitted into a build stage")
	}
	// The message must say what was missing and what the pipeline produces: without the second,
	// one looks for the mistake in the role rather than in the step.
	for _, expected := range []string{"build", "unit-tests", "image", "results"} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("reason %q — it must carry %q", err, expected)
		}
	}
}

// An action in a stage with a role is refused.
//
// The most insidious case: a failed webhook would fail the release, although it has nothing
// to say about its quality.
func TestAnActionDoesNotEnterAStageWithARole(t *testing.T) {
	handler := &AutomationsHandler{catalog: roleCatalogue()}

	err := handler.checkRoles(planWithStage(plan.RoleTest,
		plan.Geste{Kind: plan.GesteAction, Ref: "webhook"}))
	if err == nil {
		t.Fatal("an action was admitted into a stage with a role")
	}
	if !strings.Contains(err.Error(), "quality") {
		t.Errorf("reason %q — it must say why it is refused", err)
	}
}

// A stage with no role carries what it likes.
//
// It is what allows warning a team without putting the release's state at stake: the defect
// the stage role corrects.
func TestAStageWithNoRoleCarriesWhatItLikes(t *testing.T) {
	handler := &AutomationsHandler{catalog: roleCatalogue()}

	err := handler.checkRoles(planWithStage("",
		plan.Geste{Kind: plan.GesteAction, Ref: "webhook"},
		pipelineGeste("unit-tests")))
	if err != nil {
		t.Errorf("a stage with no role was refused: %v", err)
	}
}

// Both roles accept what matches them.
func TestEachRoleAdmitsWhatMatchesIt(t *testing.T) {
	handler := &AutomationsHandler{catalog: roleCatalogue()}

	for role, ref := range map[plan.Role]string{
		plan.RoleBuild: "build", plan.RoleTest: "unit-tests",
	} {
		if err := handler.checkRoles(planWithStage(role, pipelineGeste(ref))); err != nil {
			t.Errorf("role %q with %q refused: %v", role, ref, err)
		}
	}
}

/*
 * The catalogue's wire format.
 *
 * The keys this response carries are read by the interface, and nothing else names them: a
 * rename here compiles, passes every other test, and empties a screen. It happened — the
 * `gestes` key became `steps` during a translation pass, and the editor crashed on an
 * undefined list.
 */

// catalogueStore is the minimum the catalogue and automation routes read.
type catalogueStore struct{ AutomationStore }

func (catalogueStore) PluginSettingsAll(context.Context) (map[string]store.PluginSettings, error) {
	return map[string]store.PluginSettings{}, nil
}

func (catalogueStore) ProjectBySlug(context.Context, string) (store.Project, error) {
	return store.Project{ID: 7, Slug: "mse"}, nil
}

func (catalogueStore) ReleaseAutomation(context.Context, int64) (store.ReleaseAutomation, error) {
	return store.ReleaseAutomation{
		ProjectID: 7, UpdatedBy: "alice",
		Plan: plan.Plan{Stages: []plan.Stage{{Role: plan.RoleBuild, Gestes: []plan.Geste{
			{Kind: plan.GestePipeline, Ref: "build", Label: "portal"},
		}}}},
	}, nil
}

// The catalogue carries the keys the editor reads, under those names.
func TestTheCatalogueCarriesTheKeysTheEditorReads(t *testing.T) {
	handler := NewAutomationsHandler(catalogueStore{}, roleCatalogue(), quietLogger())

	recorder := httptest.NewRecorder()
	handler.Catalogue(recorder, httptest.NewRequest(stdhttp.MethodGet, "/admin/automations/catalogue", nil))

	if recorder.Code != stdhttp.StatusOK {
		t.Fatalf("code = %d, expected 200: %s", recorder.Code, recorder.Body)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("unreadable response: %v", err)
	}
	// The four names the interface asks for. `gestes` stays French, like the rest of the wire
	// format it consumes.
	for _, key := range []string{"events", "gestes", "when", "roles"} {
		if _, found := body[key]; !found {
			t.Errorf("key %q missing — the screen that reads it would crash on an undefined list", key)
		}
	}
	// And each is a list: the editor iterates over them with no guard.
	for key, raw := range body {
		if !strings.HasPrefix(string(raw), "[") {
			t.Errorf("key %q = %s, expected an array", key, raw)
		}
	}
}

// The automation carries the keys the same screen reads, and a plan the editor can walk.
//
// `plan.stages[].gestes` is the shape the editor iterates over with no guard: a stage with no
// step list would crash it exactly as the catalogue did.
func TestTheAutomationCarriesThePlanTheEditorWalks(t *testing.T) {
	handler := NewAutomationsHandler(catalogueStore{}, roleCatalogue(), quietLogger())

	request := httptest.NewRequest(stdhttp.MethodGet, "/projects/mse/automation", nil)
	request.SetPathValue("project", "mse")
	recorder := httptest.NewRecorder()
	handler.Automation(recorder, request)

	if recorder.Code != stdhttp.StatusOK {
		t.Fatalf("code = %d, expected 200: %s", recorder.Code, recorder.Body)
	}
	var body struct {
		Plan struct {
			Stages []struct {
				Role   string            `json:"role"`
				Gestes []json.RawMessage `json:"gestes"`
			} `json:"stages"`
		} `json:"plan"`
		Declared  bool   `json:"declared"`
		UpdatedBy string `json:"updated_by"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("unreadable response: %v", err)
	}
	if !body.Declared || body.UpdatedBy != "alice" {
		t.Errorf("declared = %v, updated_by = %q", body.Declared, body.UpdatedBy)
	}
	if len(body.Plan.Stages) != 1 {
		t.Fatalf("%d stage(s), expected 1: %s", len(body.Plan.Stages), recorder.Body)
	}
	if body.Plan.Stages[0].Role == "" || len(body.Plan.Stages[0].Gestes) != 1 {
		t.Errorf("stage = %+v — the editor reads `role` and `gestes`", body.Plan.Stages[0])
	}
}
