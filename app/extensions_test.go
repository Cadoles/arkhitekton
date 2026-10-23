package app

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/host"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// withExtensions sets a distribution's extensions for the duration of a test.
func withExtensions(t *testing.T, given ...host.Extension) {
	t.Helper()
	previous := extensions
	t.Cleanup(func() { extensions = previous })
	if err := setExtensions(given); err != nil {
		t.Fatal(err)
	}
}

type promoted struct{ name string }

func (p promoted) Promoted(at time.Time, linked host.JobReleaseContext) host.Event {
	return host.Event{Type: host.EventType(p.name), Subject: linked.Tag, OccurredAt: at}
}

func widgets() host.Extension {
	return host.Extension{
		ID: "widgets",
		Plugin: capability.Plugin{ID: "widgets", Version: "1.0.0", APIVersion: sdk.APIVersion, Label: "Widgets",
			Capabilities: []capability.Header{{Kind: capability.KindModule, Name: "widgets", Label: "Widgets"}}},
		Stacks: map[string][]capability.Manifest{"php": {{Kind: "Task", Name: "widget-task"}}},
		Migrations: &host.MigrationSet{Name: "widgets", Table: "goose_widgets_db_version", Files: fstest.MapFS{
			"0001_widgets.sql": {Data: []byte("-- +goose Up\ncreate table widgets (id bigint);\n-- +goose Down\ndrop table widgets;\n")},
		}},
		Seed: host.Seed{SchedulerConfig: func() map[string]string { return map[string]string{"widgets.repoUrl": "ssh://w"} }},
	}
}

func TestAnExtensionJoinsTheBuiltInPlugins(t *testing.T) {
	withExtensions(t, widgets())

	registry, err := carriedRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Plugin("widgets"); err != nil {
		t.Error(err)
	}
	if _, err := registry.Plugin("php"); err != nil {
		t.Error("the built-in plugins left out")
	}
	if names := carriedStacks()["php"]; !slices.ContainsFunc(names, func(m capability.Manifest) bool { return m.Name == "widget-task" }) {
		t.Errorf("php stack = %v", names)
	}
	if extensionSchedulerConfig(map[string]string{"tekton.namespace": "ns"})["widgets.repoUrl"] != "ssh://w" {
		t.Error("the extension's configuration keys left out")
	}
	checks := extensionReadiness(nil, []host.APIHooks{{Readiness: []host.ReadinessCheck{{Name: "widgets-cluster"}}}})
	if len(checks) != 2 || checks[0].Name != "schema-widgets" || checks[1].Name != "widgets-cluster" {
		t.Errorf("readiness = %+v", checks)
	}
}

func TestAnAmbiguousSetOfExtensionsIsRefused(t *testing.T) {
	other := widgets()
	other.Plugin.ID = "gadgets"
	for name, given := range map[string][]host.Extension{
		"twice":          {widgets(), widgets()},
		"another plugin": {other},
		"no id":          {{}},
	} {
		if err := setExtensions(given); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	extensions = nil
}

func TestTheExtensionsHooksAreComposed(t *testing.T) {
	refuse := errors.New("calendar versions cannot be routed")
	hooks := []host.APIHooks{
		{
			Deployments: func(context.Context, int64) (map[string][]string, error) {
				return map[string][]string{"1.0.0": {"qualif"}}, nil
			},
			EnvironmentSuggestions: func(context.Context, string) []host.Suggestion { return []host.Suggestion{{Value: "qualif"}} },
			SchemeCheck:            func(context.Context, host.Scheme) error { return refuse },
			Routes:                 []host.Mount{func(*http.ServeMux, *host.Middleware) {}},
		},
		{
			Deployments: func(context.Context, int64) (map[string][]string, error) {
				return map[string][]string{"1.0.0": {"prod"}}, nil
			},
			SchemeCheck: func(context.Context, host.Scheme) error { return nil },
		},
	}

	deployed, err := composedDeployments(hooks)(context.Background(), 1)
	if err != nil || !slices.Equal(deployed["1.0.0"], []string{"qualif", "prod"}) {
		t.Errorf("deployments %v, %v", deployed, err)
	}
	if suggested := composedSuggestions(hooks)(context.Background(), "p"); len(suggested) != 1 {
		t.Errorf("suggestions = %v", suggested)
	}
	if err := composedSchemeCheck(hooks)(context.Background(), nil); !errors.Is(err, refuse) {
		t.Errorf("scheme check: %v", err)
	}
	if len(composedRoutes(hooks)) != 1 {
		t.Error("routes left out")
	}
	if composedPromotions(hooks) != nil || composedSuggestions(nil) != nil || composedSchemeCheck(nil) != nil {
		t.Error("a hook nobody gives is not nil: the core would take it for one")
	}

	settings := []host.WorkerHooks{
		{ProjectSettings: func(context.Context, int64) (map[string]string, error) { return map[string]string{"a": "1"}, nil }},
		{ProjectSettings: func(context.Context, int64) (map[string]string, error) { return map[string]string{"b": "2"}, nil }},
	}
	if merged, err := composedProjectSettings(settings)(context.Background(), 1); err != nil || len(merged) != 2 {
		t.Errorf("settings %v, %v", merged, err)
	}
	if _, err := composedPromotionFacts([]host.WorkerHooks{{PromotionFacts: promoted{"a"}}, {PromotionFacts: promoted{"b"}}}); err == nil {
		t.Error("two statements of one promotion accepted")
	}
}

func TestAnExtensionsFailureNamesIt(t *testing.T) {
	broken := widgets()
	broken.API = func(host.APIHost) (host.APIHooks, error) { return host.APIHooks{}, errors.New("no cluster") }
	withExtensions(t, broken)
	if _, err := apiHooks(host.APIHost{}); err == nil || !strings.Contains(err.Error(), `"widgets"`) {
		t.Errorf("err = %v", err)
	}
}

// An extension's module is refused unless its plugin declares one: its switch would control nothing.
func TestAModuleNeedsItsPluginsDeclaration(t *testing.T) {
	undeclared := widgets()
	undeclared.Plugin.Capabilities = nil
	withExtensions(t, undeclared)
	registry, err := carriedRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hostModules(registry, widgetModule{}); err == nil {
		t.Error("a module its plugin does not declare accepted")
	}
}

type widgetModule struct{}

func (widgetModule) Plugin() string                              { return "widgets" }
func (widgetModule) Run(context.Context, host.ModuleState) error { return nil }
