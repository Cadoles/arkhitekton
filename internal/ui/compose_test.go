package ui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Cadoles/arkhitekton/sdk/manifest"
	schema "github.com/Cadoles/arkhitekton/sdk/ui"
)

const coreYAML = `
apiVersion: ui.arkhitekton/v1
kind: Screen
metadata: { name: shell }
spec:
  path: /
  blocks:
    - { id: core.shell, component: core/shell }
---
apiVersion: ui.arkhitekton/v1
kind: Screen
metadata: { name: release }
spec:
  path: releases/:tag
  parent: shell
  blocks:
    - { id: core.header, component: core/release-header, slots: { badges: [] } }
    - { id: core.rebuild, component: core/release-rebuild, permission: pipeline:run }
    - { id: core.artifacts, component: core/release-artifacts }
    - { id: core.notes, component: core/release-notes }
---
apiVersion: ui.arkhitekton/v1
kind: Navigation
metadata: { name: main }
spec:
  items:
    - { id: core.versions, label: Versions, icon: tag, to: /versions }
    - { id: core.admin, label: Administration, icon: cog, to: /admin, permission: admin }
---
apiVersion: ui.arkhitekton/v1
kind: Widget
metadata: { name: core.state }
spec:
  params: { value: { required: true } }
  widget: { badge: { text: "{value}" } }
`

const deployYAML = `
apiVersion: ui.arkhitekton/v1
kind: Screen
metadata: { name: deploy.environments }
spec:
  path: environments
  parent: shell
  data: { environments: { source: plugin, path: environments } }
  blocks:
    - { id: deploy.list, component: deploy/environments }
---
apiVersion: ui.arkhitekton/v1
kind: Screen
metadata: { name: release }
spec:
  blocks:
    - id: deploy.deployment
      after: core.artifacts
      data: { deployment: { source: plugin, path: "releases/{route.tag}/deployment" } }
      widget:
        table:
          rows: deployment.environments
          columns:
            - { label: Environnement, value: "{label}" }
            - label: ""
              action: { label: Promouvoir, method: POST, path: "releases/{route.tag}/promote/{id}" }
            - label: ""
              action: { label: Valider, method: PUT, path: "releases/{route.tag}/signoff" }
    - id: core.header
      slots: { badges: [{ id: deploy.deployed, component: deploy/deployed-badges }] }
---
apiVersion: ui.arkhitekton/v1
kind: Navigation
metadata: { name: main }
spec:
  home: /environments
  items:
    - { id: deploy.environments, before: core.versions, label: Environnements, icon: server, to: /environments }
`

var deployRoutes = []manifest.Route{
	{Method: "GET", Path: "environments", Permission: "read"},
	{Method: "GET", Path: "releases/{tag}/deployment", Permission: "read"},
	{Method: "POST", Path: "releases/{tag}/promote/{environment}", Permission: "deploy:{environment}"},
	{Method: "PUT", Path: "releases/{tag}/signoff", Permission: "release:edit"},
}

func read(t *testing.T, source string) []schema.Resource {
	t.Helper()
	resources, err := Read([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	return resources
}

var options = Options{KnownPermission: func(permission string) bool {
	return slices.Contains([]string{"read", "admin", "pipeline:run", "release:edit"}, permission) ||
		strings.HasPrefix(permission, "deploy:")
}}

func compose(t *testing.T, contributions ...Contribution) *Interface {
	t.Helper()
	composed, refused := Compose(read(t, coreYAML), contributions, options)
	if len(refused) > 0 {
		t.Fatal(errors.Join(refused...))
	}
	return composed
}

func everything(Viewer) Viewer {
	return Viewer{Active: func(string) bool { return true }, Can: func(string) bool { return true }}
}

func screenOf(view View, name string) *ScreenView {
	for i := range view.Screens {
		if view.Screens[i].Name == name {
			return &view.Screens[i]
		}
	}
	return nil
}

func ids(blocks []schema.Block) []string {
	var found []string
	for _, block := range blocks {
		found = append(found, block.ID)
	}
	return found
}

func TestAPluginAddsToTheCoresScreensAndMenus(t *testing.T) {
	view := compose(t, Contribution{Plugin: "deploy", Resources: read(t, deployYAML), Routes: deployRoutes}).For(everything(Viewer{}))

	release := screenOf(view, "release")
	if got := ids(release.Blocks); !slices.Equal(got, []string{"core.header", "core.rebuild", "core.artifacts", "deploy.deployment", "core.notes"}) {
		t.Errorf("release blocks = %v", got)
	}
	if badges := release.Blocks[0].Slots["badges"]; len(badges) != 1 || badges[0].Component != "deploy/deployed-badges" {
		t.Errorf("badges = %+v", badges)
	}
	deployment := release.Blocks[3]
	if source := deployment.Data["deployment"]; source.Plugin != "deploy" || source.Scope != "project" {
		t.Errorf("source = %+v", source)
	}
	if action := deployment.Widget.Table.Columns[1].Action; action.Plugin != "deploy" || action.Permission != "deploy:{environment}" {
		t.Errorf("action = %+v", action)
	}

	main := view.Navigations["main"]
	if main.Home != "/environments" || main.Items[0].ID != "deploy.environments" {
		t.Errorf("main = %+v", main)
	}
	if screenOf(view, "deploy.environments") == nil {
		t.Error("the plugin's screen is missing")
	}
}

func TestWhatAnInactivePluginBringsIsLeftOut(t *testing.T) {
	composed := compose(t, Contribution{Plugin: "deploy", Resources: read(t, deployYAML), Routes: deployRoutes})
	view := composed.For(Viewer{Active: func(string) bool { return false }, Can: func(string) bool { return true }})

	if screenOf(view, "deploy.environments") != nil {
		t.Error("the screen of an inactive plugin is served")
	}
	release := screenOf(view, "release")
	if got := ids(release.Blocks); slices.Contains(got, "deploy.deployment") {
		t.Errorf("release blocks = %v", got)
	}
	if badges := release.Blocks[0].Slots["badges"]; len(badges) != 0 {
		t.Errorf("badges = %+v", badges)
	}
	if main := view.Navigations["main"]; main.Home != "" || len(main.Items) != 2 {
		t.Errorf("main = %+v", main)
	}
}

func TestWhatTheViewerMayNotDoIsLeftOut(t *testing.T) {
	composed := compose(t, Contribution{Plugin: "deploy", Resources: read(t, deployYAML), Routes: deployRoutes})
	view := composed.For(Viewer{Active: func(string) bool { return true }, Can: func(permission string) bool { return permission == "read" }})

	release := screenOf(view, "release")
	if got := ids(release.Blocks); slices.Contains(got, "core.rebuild") {
		t.Errorf("release blocks = %v", got)
	}
	// The promotion's permission depends on the row: the data tells. The sign-off's does not.
	columns := release.Blocks[2].Widget.Table.Columns
	if len(columns) != 2 || columns[1].Action.Label != "Promouvoir" {
		t.Errorf("columns = %+v", columns)
	}
	if items := view.Navigations["main"].Items; len(items) != 2 || items[1].ID != "core.versions" {
		t.Errorf("items = %+v", items)
	}
	// The composed interface is not changed by a viewer's filter.
	if all := composed.For(everything(Viewer{})); len(screenOf(all, "release").Blocks[3].Widget.Table.Columns) != 3 {
		t.Error("filtering changed the interface")
	}
}

func TestAContributionThatDoesNotComposeIsRefusedWithItsReason(t *testing.T) {
	contribution := func(source string, routes ...manifest.Route) Contribution {
		return Contribution{Plugin: "deploy", Resources: read(t, source), Routes: append(routes, deployRoutes...)}
	}
	screen := func(name, spec string) string {
		return "apiVersion: ui.arkhitekton/v1\nkind: Screen\nmetadata: { name: " + name + " }\nspec:\n" + spec
	}
	for expected, source := range map[string]string{
		"no core screen":              screen("history", "  path: history\n  parent: shell\n  blocks: [{ id: deploy.a, component: deploy/a }]"),
		"already defined":             screen("deploy.x", "  path: one\n  parent: shell") + "\n---\n" + screen("deploy.x", "  path: two\n  parent: shell"),
		"path is owned by core":       screen("release", "  path: elsewhere"),
		`"deploy", a block of deploy`: screen("release", "  blocks: [{ id: deploy, component: deploy/a }]"),
		"only its slots":              screen("release", "  blocks: [{ id: core.notes, component: deploy/a }]"),
		`no slot "rows"`:              screen("release", "  blocks: [{ id: core.header, slots: { rows: [{ id: deploy.a, component: deploy/a }] } }]"),
		"no such entry":               screen("release", "  blocks: [{ id: deploy.a, after: core.absent, component: deploy/a }]"),
		"core's or its own":           screen("release", "  blocks: [{ id: deploy.a, after: other.b, component: deploy/a }]"),
		"belongs to other":            screen("release", "  blocks: [{ id: deploy.a, component: other/a }]"),
		"declares no such route":      screen("release", "  blocks: [{ id: deploy.a, widget: { action: { label: A, method: DELETE, path: environments } } }]"),
		"GET nothing":                 screen("release", "  blocks: [{ id: deploy.a, data: { x: { source: plugin, path: nothing } }, component: deploy/a }]"),
		"no role grants":              screen("release", "  blocks: [{ id: deploy.a, permission: typo, component: deploy/a }]"),
		"same route":                  screen("deploy.a", "  path: releases/:tag\n  parent: shell"),
		"no such screen":              screen("deploy.a", "  path: a\n  parent: absent"),
		"no such widget":              screen("release", "  blocks: [{ id: deploy.a, widget: { use: { widget: core.absent } } }]"),
		"parameter value is required": screen("release", "  blocks: [{ id: deploy.a, widget: { use: { widget: core.state } } }]"),
		"no such navigation":          screen("release", "  blocks: [{ id: deploy.a, widget: { tabs: { navigation: absent } } }]"),
		"not added to":                "apiVersion: ui.arkhitekton/v1\nkind: Widget\nmetadata: { name: core.state }\nspec: { widget: { empty: { text: x } } }",
	} {
		err := Check(read(t, coreYAML), nil, contribution(source), options)
		if err == nil || !strings.Contains(err.Error(), expected) {
			t.Errorf("%q: %v", expected, err)
		}
	}
}

// Two plugins both setting the home: the second is refused, naming the first.
func TestASecondOwnerOfAFieldIsRefused(t *testing.T) {
	home := func(plugin, to string) Contribution {
		return Contribution{Plugin: plugin, Resources: read(t, "apiVersion: ui.arkhitekton/v1\nkind: Navigation\nmetadata: { name: main }\nspec: { home: "+to+" }")}
	}
	err := Check(read(t, coreYAML), []Contribution{home("deploy", "/environments")}, home("other", "/other"), options)
	if err == nil || !strings.Contains(err.Error(), "home is owned by deploy") {
		t.Errorf("err = %v", err)
	}
	// Another version of the same plugin replaces it rather than conflicting.
	if err := Check(read(t, coreYAML), []Contribution{home("deploy", "/environments")}, home("deploy", "/elsewhere"), options); err != nil {
		t.Errorf("an update conflicts with itself: %v", err)
	}

	composed, refused := Compose(read(t, coreYAML), []Contribution{home("other", "/other"), home("deploy", "/environments")}, options)
	var pluginError *PluginError
	if len(refused) != 1 || !errors.As(refused[0], &pluginError) || pluginError.Plugin != "other" {
		t.Fatalf("refused = %v", refused)
	}
	if view := composed.For(everything(Viewer{})); view.Navigations["main"].Home != "/environments" {
		t.Errorf("home = %q", view.Navigations["main"].Home)
	}
}

func TestTheCoresOwnResourcesMustCompose(t *testing.T) {
	broken := append(read(t, coreYAML), read(t, "apiVersion: ui.arkhitekton/v1\nkind: Screen\nmetadata: { name: lost }\nspec: { path: x, parent: absent }")...)
	if _, refused := Compose(broken, nil, options); len(refused) != 1 || !strings.Contains(refused[0].Error(), "the core's interface") {
		t.Errorf("refused = %v", refused)
	}
}
