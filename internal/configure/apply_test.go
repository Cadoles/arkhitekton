package configure

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/configfile"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

func (i instance) applier(env map[string]string) Applier {
	return Applier{
		Services: i.services(), Store: i.db, Registry: i.registry, Catalog: i.catalog, Actor: "config-apply",
		Getenv: func(name string) (string, bool) {
			value, found := env[name]
			return value, found
		},
	}
}

func (i instance) export(t *testing.T) []byte {
	t.Helper()
	exported, err := Exporter{Store: i.db, Registry: i.registry}.Export(context.Background())
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	raw, err := configfile.Encode(exported.Set)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return raw
}

func read(t *testing.T, raw []byte) configfile.Set {
	t.Helper()
	set, err := configfile.Read(raw, "test.yaml")
	if err != nil {
		t.Fatalf("read: %v\n%s", err, raw)
	}
	return set
}

func descriptions(ops []Op) string {
	lines := make([]string, 0, len(ops))
	for _, op := range ops {
		lines = append(lines, op.Description)
	}
	return strings.Join(lines, "\n")
}

func planOps(t *testing.T, applier Applier, set configfile.Set, prune bool) ([]Op, []string) {
	t.Helper()
	ops, warnings, err := applier.Plan(context.Background(), set, prune)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return ops, warnings
}

func apply(t *testing.T, applier Applier, set configfile.Set, prune bool) {
	t.Helper()
	ops, _ := planOps(t, applier, set, prune)
	if err := applier.Execute(context.Background(), ops); err != nil {
		t.Fatalf("apply: %v\nops:\n%s", err, descriptions(ops))
	}
}

var secretValues = map[string]string{
	"ARK_GITEA_TOKEN":                 "token-from-the-environment",
	"ARK_PORTAL_GITEA_WEBHOOK_SECRET": "webhook-secret-from-the-environment",
}

// An export applied to an empty instance rebuilds the same configuration, and applying it again
// writes nothing.
func TestAnExportAppliedToAnEmptyInstanceRebuildsIt(t *testing.T) {
	exported := configured(t).export(t)

	fresh := emptyInstance(t)
	applier := fresh.applier(secretValues)
	apply(t, applier, read(t, exported), false)

	if rebuilt := fresh.export(t); string(rebuilt) != string(exported) {
		t.Errorf("rebuilt configuration differs:\n%s\n---\n%s", exported, rebuilt)
	}

	// The secrets were resolved and sealed: a second apply, without the variables, keeps them.
	ops, warnings := planOps(t, fresh.applier(nil), read(t, exported), false)
	if len(ops) != 0 {
		t.Errorf("a second apply writes:\n%s", descriptions(ops))
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "keeps its recorded value") {
		t.Errorf("warnings = %v, want the kept secrets named", warnings)
	}
	settings, _ := fresh.db.PluginSettingsAll(context.Background())
	token := settings["gitea"].Config["token"]
	if opened, err := fresh.cipher.Open(token); err != nil || opened != "token-from-the-environment" {
		t.Errorf("token = %q (%v), want the environment's value, sealed", opened, err)
	}
}

// A configuration applied to the instance it was exported from changes nothing.
func TestAnInstanceItsOwnExportChangesNothing(t *testing.T) {
	i := configured(t)
	ops, _ := planOps(t, i.applier(nil), read(t, i.export(t)), true)
	if len(ops) != 0 {
		t.Errorf("applying an instance's own export writes:\n%s", descriptions(ops))
	}
}

// Without the prune, what the files no longer declare stays; with it, it goes.
func TestWhatIsNoLongerDeclaredGoesOnlyWithThePrune(t *testing.T) {
	i := configured(t)
	set := read(t, i.export(t))
	project := &set.Projects[0]
	project.Reactions = nil
	project.Pipelines = nil
	project.TrackerStatuses = nil

	if ops, _ := planOps(t, i.applier(nil), set, false); len(ops) != 0 {
		t.Errorf("without the prune:\n%s", descriptions(ops))
	}

	applier := i.applier(nil)
	ops, _ := planOps(t, applier, set, true)
	got := descriptions(ops)
	for _, want := range []string{"- reaction portal/Analyse à chaque push", "- pipeline portal/php/static-tests", "- tracker status portal/En recette"} {
		if !strings.Contains(got, want) {
			t.Errorf("prune plan lacks %q:\n%s", want, got)
		}
	}
	if err := applier.Execute(context.Background(), ops); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if again, _ := planOps(t, applier, set, true); len(again) != 0 {
		t.Errorf("after the prune:\n%s", descriptions(again))
	}
}

// A renamed reaction is a new one: the old one stays without the prune, and the plan says so.
func TestARenamedReactionIsANewOne(t *testing.T) {
	i := configured(t)
	set := read(t, i.export(t))
	set.Projects[0].Reactions[0].Name = "Analyse renommée"

	ops, _ := planOps(t, i.applier(nil), set, true)
	got := descriptions(ops)
	if !strings.Contains(got, "+ reaction portal/Analyse renommée") || !strings.Contains(got, "- reaction portal/Analyse à chaque push") {
		t.Errorf("rename plan:\n%s", got)
	}
}

// A change made in a file is written, and only that change.
func TestAChangeIsWrittenAlone(t *testing.T) {
	i := configured(t)
	set := read(t, i.export(t))
	set.Projects[0].Label = "Portail"
	set.Projects[0].Schedules[0].Cron = "0 4 * * *"
	set.Instance.QueueLimits["php/static-tests"] = 3

	applier := i.applier(nil)
	ops, _ := planOps(t, applier, set, false)
	want := "~ queue limit php/static-tests: 3\n~ project portal: label\n~ schedule portal/Nightly"
	if got := descriptions(ops); got != want {
		t.Errorf("plan:\n%s\nwant:\n%s", got, want)
	}
	if err := applier.Execute(context.Background(), ops); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if again, _ := planOps(t, applier, set, false); len(again) != 0 {
		t.Errorf("after the change:\n%s", descriptions(again))
	}
}

func TestWhatCannotBeAppliedIsRefusedBeforeAnyWrite(t *testing.T) {
	cases := map[string]struct {
		edit func(set *configfile.Set)
		want string
	}{
		"kind missing from the catalogue": {
			edit: func(set *configfile.Set) { set.Projects[0].Reactions[0].Plan = buildPlan("php/unknown") },
			want: "missing from the catalogue",
		},
		"fact nobody produces": {
			edit: func(set *configfile.Set) { set.Projects[0].Reactions[0].Event = "gitea.tag" },
			want: "produces",
		},
		"secret written in clear": {
			edit: func(set *configfile.Set) {
				set.Instance.Plugins["gitea"].Config["token"] = configfile.Value{Plain: "in-the-file"}
			},
			want: "never its value",
		},
		"unknown plugin": {
			edit: func(set *configfile.Set) {
				set.Projects[0].Plugins["jenkins"] = configfile.ProjectPlugin{}
			},
			want: "jenkins",
		},
	}
	i := configured(t)
	exported := i.export(t)
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			set := read(t, exported)
			test.edit(&set)
			_, _, err := i.applier(nil).Plan(context.Background(), set, false)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Errorf("error = %v, want one naming %q", err, test.want)
			}
		})
	}
}

// A secret declared by reference but neither set in the environment nor recorded is refused.
func TestASecretWithNoValueAnywhereIsRefused(t *testing.T) {
	exported := configured(t).export(t)
	fresh := emptyInstance(t)
	_, _, err := fresh.applier(nil).Plan(context.Background(), read(t, exported), false)
	if err == nil || !strings.Contains(err.Error(), "ARK_GITEA_TOKEN is not set and no value is recorded") {
		t.Errorf("error = %v", err)
	}
}

// Only what is written is checked: an entity the files leave as it is does not block an apply,
// even when it names a kind the instance has lost since; changed, it is refused.
func TestOnlyWhatIsWrittenIsChecked(t *testing.T) {
	i := configured(t)
	set := read(t, i.export(t))
	i.catalog = &catalog.Catalog{Pipelines: []catalog.Pipeline{
		{Kind: "docker/build", PipelineRef: "build-image", Produces: []catalog.Product{catalog.ProductImage}},
	}}

	if ops, _ := planOps(t, i.applier(nil), set, false); len(ops) != 0 {
		t.Errorf("an unchanged configuration writes:\n%s", descriptions(ops))
	}

	set.Projects[0].Reactions[0].Match = map[string]string{"branch": "develop"}
	_, _, err := i.applier(nil).Plan(context.Background(), set, false)
	if err == nil || !strings.Contains(err.Error(), `"php/static-tests" is missing from the catalogue`) {
		t.Errorf("error = %v, want the changed reaction's lost kind named", err)
	}
}

// A kind the same files bind is known to their plans: the binding is written before the projects.
func TestAKindTheFilesBindIsKnownToTheirPlans(t *testing.T) {
	i := configured(t)
	set := read(t, i.export(t))
	i.catalog = &catalog.Catalog{Pipelines: []catalog.Pipeline{
		{Kind: "docker/build", PipelineRef: "build-image", Produces: []catalog.Product{catalog.ProductImage}},
	}}
	set.Projects[0].Reactions[0].Match = map[string]string{"branch": "develop"}
	registry, err := capabilities.New(forgePlugin(), capability.Plugin{ID: "php", Version: "1", APIVersion: sdk.APIVersion,
		Pipelines: []capability.Pipeline{{Name: "static-tests", Spec: capability.PipelineSpec{PipelineRef: "php-static"}}}})
	if err != nil {
		t.Fatal(err)
	}
	i.registry = registry
	if set.Instance == nil {
		t.Fatal("no instance exported")
	}
	set.Instance.CapabilityBindings = map[string]json.RawMessage{"php/static-tests": json.RawMessage(`{}`)}

	ops, _, err := i.applier(nil).Plan(context.Background(), set, false)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !strings.Contains(descriptions(ops), "~ binding php/static-tests") {
		t.Errorf("ops:\n%s", descriptions(ops))
	}
}

// A file names a binding's capability by its key; the recorded one also carries it as `ref`. The same
// binding either way writes nothing.
func TestABindingWithoutItsRefIsTheRecordedOne(t *testing.T) {
	recorded := json.RawMessage(`{"ref": "php/static-tests", "timeout": "45m"}`)
	if !sameBinding("php/static-tests", recorded, json.RawMessage(`{"timeout": "45m"}`)) {
		t.Error("the same binding read as a change")
	}
	if sameBinding("php/static-tests", recorded, json.RawMessage(`{"timeout": "30m"}`)) {
		t.Error("a changed timeout read as the same binding")
	}
}

// A switch is the file's when the automation is created, and the interface's afterwards: an apply
// neither switches back on what was switched off by hand, nor off what was switched on.
func TestASwitchIsTheFilesAtCreationOnly(t *testing.T) {
	ctx := context.Background()
	i := configured(t)
	exported := read(t, i.export(t))
	project, err := i.db.ProjectBySlug(ctx, "portal")
	if err != nil {
		t.Fatal(err)
	}
	reactions, err := i.db.ListReactions(ctx, project.ID)
	if err != nil || len(reactions) == 0 {
		t.Fatalf("reactions: %v (%v)", reactions, err)
	}
	if err := i.db.SetReactionEnabled(ctx, project.ID, reactions[0].ID, false); err != nil {
		t.Fatal(err)
	}
	if err := i.db.SetReleaseAutomationEnabled(ctx, project.ID, false); err != nil {
		t.Fatal(err)
	}
	if ops, _ := planOps(t, i.applier(nil), exported, false); len(ops) != 0 {
		t.Errorf("an apply switches back on what was switched off by hand:\n%s", descriptions(ops))
	}

	off := false
	set := read(t, i.export(t))
	declared := &set.Projects[0]
	for index := range declared.Reactions {
		declared.Reactions[index].Enabled = nil
	}
	for index := range declared.Schedules {
		declared.Schedules[index].Enabled = &off
	}
	declared.ReleaseAutomation.Enabled = nil
	if ops, _ := planOps(t, i.applier(nil), set, false); len(ops) != 0 {
		t.Errorf("only switches differ, and the apply writes:\n%s", descriptions(ops))
	}

	added := declared.Reactions[0]
	added.Name, added.Enabled = "Analyse livrée éteinte", &off
	declared.Reactions = append(declared.Reactions, added)
	applier := i.applier(nil)
	ops, _ := planOps(t, applier, set, false)
	if got := descriptions(ops); got != "+ reaction portal/Analyse livrée éteinte (switched off)" {
		t.Errorf("plan:\n%s", got)
	}
	if err := applier.Execute(ctx, ops); err != nil {
		t.Fatal(err)
	}
	reactions, _ = i.db.ListReactions(ctx, project.ID)
	for _, entry := range reactions {
		if entry.Name == added.Name && entry.Enabled {
			t.Errorf("%q created switched on", entry.Name)
		}
	}
}

// A release automation declared switched off is created switched off.
func TestAReleaseAutomationDeclaredOffIsCreatedOff(t *testing.T) {
	ctx := context.Background()
	set := read(t, configured(t).export(t))
	off := false
	set.Projects[0].ReleaseAutomation.Enabled = &off

	fresh := emptyInstance(t)
	applier := fresh.applier(secretValues)
	ops, _ := planOps(t, applier, set, false)
	if got := descriptions(ops); !strings.Contains(got, "+ release automation portal (switched off)") {
		t.Errorf("plan:\n%s", got)
	}
	if err := applier.Execute(ctx, ops); err != nil {
		t.Fatal(err)
	}
	project, err := fresh.db.ProjectBySlug(ctx, "portal")
	if err != nil {
		t.Fatal(err)
	}
	automation, err := fresh.db.ReleaseAutomation(ctx, project.ID)
	if err != nil || automation.Enabled || len(automation.Plan.Stages) == 0 {
		t.Errorf("automation = %+v (%v), expected its plan, switched off", automation, err)
	}
}

// A resolved sensitive value is compared with the one sealed in place: the same value writes
// nothing, so that a diff can say there is nothing to do; another value is written.
func TestAResolvedSecretIsWrittenOnlyWhenItChanged(t *testing.T) {
	exported := configured(t).export(t)
	fresh := emptyInstance(t)
	apply(t, fresh.applier(secretValues), read(t, exported), false)

	if ops, _ := planOps(t, fresh.applier(secretValues), read(t, exported), false); len(ops) != 0 {
		t.Errorf("the same values write:\n%s", descriptions(ops))
	}

	changed := map[string]string{}
	for name, value := range secretValues {
		changed[name] = value
	}
	changed["ARK_GITEA_TOKEN"] = "a-rotated-token"
	applier := fresh.applier(changed)
	ops, _ := planOps(t, applier, read(t, exported), false)
	if got := descriptions(ops); got != "~ plugin gitea" {
		t.Fatalf("plan:\n%s\nwant the token's plugin alone", got)
	}
	if err := applier.Execute(context.Background(), ops); err != nil {
		t.Fatal(err)
	}
	settings, _ := fresh.db.PluginSettingsAll(context.Background())
	if opened, err := fresh.cipher.Open(settings["gitea"].Config["token"]); err != nil || opened != "a-rotated-token" {
		t.Errorf("token = %q (%v), want the new value, sealed", opened, err)
	}
}

// A pipeline's weight is written when it changes, and only then; pruned, an undeclared one returns
// to its nature's.
func TestAScoreWeightIsAppliedOnce(t *testing.T) {
	i := configured(t)
	set := read(t, i.export(t))
	set.Instance.ScoreWeights["mstore/rgaa-audit"] = 10

	applier := i.applier(nil)
	ops, _ := planOps(t, applier, set, false)
	if got := descriptions(ops); got != "~ score weight mstore/rgaa-audit: 10" {
		t.Fatalf("plan:\n%s", got)
	}
	if err := applier.Execute(context.Background(), ops); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if again, _ := planOps(t, applier, set, false); len(again) != 0 {
		t.Errorf("after the change:\n%s", descriptions(again))
	}

	delete(set.Instance.ScoreWeights, "mstore/rgaa-audit")
	ops, _ = planOps(t, applier, set, true)
	if got := descriptions(ops); !strings.Contains(got, "- score weight mstore/rgaa-audit") {
		t.Errorf("prune plan:\n%s", got)
	}
}
