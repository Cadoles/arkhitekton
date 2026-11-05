package configure

import (
	"context"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/configfile"
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

	set.Projects[0].Reactions[0].Enabled = configfile.On(false)
	_, _, err := i.applier(nil).Plan(context.Background(), set, false)
	if err == nil || !strings.Contains(err.Error(), `"php/static-tests" is missing from the catalogue`) {
		t.Errorf("error = %v, want the changed reaction's lost kind named", err)
	}
}
