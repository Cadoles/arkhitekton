package ui

import (
	"encoding/json"
	"strings"
	"testing"
)

const releaseScreen = `{
  "apiVersion": "ui.arkhitekton/v1", "kind": "Screen", "metadata": {"name": "release"},
  "spec": {
    "path": "releases/:tag", "parent": "shell",
    "data": {"release": {"source": "core", "name": "release", "params": {"tag": "{route.tag}"}}},
    "blocks": [
      {"id": "core.header", "component": "core/release-header", "slots": {"badges": []}},
      {"id": "core.rebuild", "component": "core/release-rebuild", "permission": "pipeline:run",
       "when": [{"field": "release.build_status", "in": ["failed"]}]},
      {"id": "core.artifacts", "component": "core/release-artifacts", "slots": {"rows": []}}
    ]
  }
}`

const deploymentContribution = `{
  "apiVersion": "ui.arkhitekton/v1", "kind": "Screen", "metadata": {"name": "release"},
  "spec": {
    "blocks": [
      {"id": "mse.deployment", "after": "core.artifacts",
       "data": {"deployment": {"source": "plugin", "path": "releases/{route.tag}/deployment"}},
       "widget": {"card": {"title": "Déploiement", "children": [
         {"table": {"rows": "deployment.environments", "empty": "Aucun environnement", "columns": [
           {"label": "Environnement", "value": "{label}"},
           {"label": "Depuis", "value": "{deployed.since | age}"},
           {"label": "État", "badge": {"text": "{state}", "tone": {"from": "state", "map": {"ok": "success"}, "default": "neutral"}}},
           {"label": "", "when": [{"field": "can_deploy", "equals": true}],
            "action": {"label": "Promouvoir", "method": "POST", "path": "releases/{route.tag}/promote/{id}",
                       "confirm": {"text": "Promouvoir vers {label} ?", "challenge": "{id}"},
                       "invalidates": ["deployment"]}}
         ]}}
       ]}}},
      {"id": "core.header", "slots": {"badges": [{"id": "mse.deployed", "component": "mse/deployed-badges"}]}}
    ]
  }
}`

func TestResourcesDecode(t *testing.T) {
	for name, raw := range map[string]string{
		"screen":       releaseScreen,
		"contribution": deploymentContribution,
		"navigation": `{"apiVersion": "ui.arkhitekton/v1", "kind": "Navigation", "metadata": {"name": "main"},
		  "spec": {"home": "/environments", "items": [{"id": "mse.environments", "before": "core.versions",
		    "label": "Environnements", "icon": "server", "to": "/environments"}]}}`,
		"widget": `{"apiVersion": "ui.arkhitekton/v1", "kind": "Widget", "metadata": {"name": "mse.signoff"},
		  "spec": {"params": {"signoff": {"required": true}},
		    "widget": {"badge": {"text": "{signoff.state | upper}", "tone": "info"}}}}`,
	} {
		resource, err := Decode([]byte(raw))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		// Written back, it reads the same.
		again, err := json.Marshal(resource)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Decode(again); err != nil {
			t.Errorf("%s written back: %v\n%s", name, err, again)
		}
	}
}

func TestAContributionKnowsItIsOne(t *testing.T) {
	resource, err := Decode([]byte(deploymentContribution))
	if err != nil {
		t.Fatal(err)
	}
	if blocks := resource.Screen.Blocks; blocks[0].Contribution() || !blocks[1].Contribution() {
		t.Errorf("blocks = %+v", blocks)
	}
	if actions := resource.Screen.Blocks[0].Widget.Actions(); len(actions) != 1 || actions[0].Label != "Promouvoir" {
		t.Errorf("actions = %+v", actions)
	}
}

// Each problem is named by where it is.
func TestAMalformedResourceIsRefusedWithItsPath(t *testing.T) {
	screen := func(spec string) string {
		return `{"apiVersion": "ui.arkhitekton/v1", "kind": "Screen", "metadata": {"name": "s"}, "spec": ` + spec + `}`
	}
	for raw, expected := range map[string]string{
		`{"apiVersion": "ui.arkhitekton/v2", "kind": "Screen", "metadata": {"name": "s"}, "spec": {}}`: "apiVersion",
		`{"apiVersion": "ui.arkhitekton/v1", "kind": "Page", "metadata": {"name": "s"}, "spec": {}}`:   "kind",
		`{"apiVersion": "ui.arkhitekton/v1", "kind": "Screen", "metadata": {"name": "S"}, "spec": {}}`: "resource's name",
		screen(`{"unknown": 1}`):                              "unknown field",
		screen(`{"blocks": [{"id": "a", "component": "x"}]}`): "blocks[0].component",
		screen(`{"blocks": [{"id": "a", "component": "core/a"}, {"id": "a", "component": "core/b"}]}`):                                                                    "blocks[1].id",
		screen(`{"blocks": [{"id": "a", "after": "b", "before": "c", "component": "core/a"}]}`):                                                                           "after or before",
		screen(`{"blocks": [{"id": "a", "permission": "read"}]}`):                                                                                                         "only fills slots",
		screen(`{"blocks": [{"id": "a", "widget": {"text": {"value": "{x"}}}]}`):                                                                                          "blocks[0].widget.text.value",
		screen(`{"blocks": [{"id": "a", "widget": {"text": {"value": "{x | replace: 'a' 'b'}"}}}]}`):                                                                      "unknown filter",
		screen(`{"blocks": [{"id": "a", "widget": {"text": {"value": "x"}, "empty": {"text": "y"}}}]}`):                                                                   "exactly one",
		screen(`{"blocks": [{"id": "a", "widget": {"badge": {"text": "x", "tone": "pink"}}}]}`):                                                                           "tone",
		screen(`{"blocks": [{"id": "a", "widget": {"table": {"rows": "x", "columns": [{"label": "a"}]}}}]}`):                                                              "columns[0]",
		screen(`{"blocks": [{"id": "a", "widget": {"action": {"label": "a", "method": "GET", "path": "x"}}}]}`):                                                           "method",
		screen(`{"blocks": [{"id": "a", "widget": {"action": {"label": "a", "method": "POST", "path": "x", "permission": "admin"}}}]}`):                                   "permission",
		screen(`{"blocks": [{"id": "a", "widget": {"action": {"label": "a", "method": "POST", "path": "x", "form": [{"name": "f", "label": "F", "type": "select"}]}}}]}`): "oneOf",
		screen(`{"blocks": [{"id": "a", "when": [{"field": "x", "equals": 1, "present": true}], "component": "core/a"}]}`):                                                "exactly one of equals",
		screen(`{"data": {"x": {"source": "core", "name": "nothing"}}}`):                                                                                                  "no core source",
		screen(`{"data": {"x": {"source": "core", "name": "release"}}}`):                                                                                                  "params.tag",
		screen(`{"data": {"x": {"source": "plugin", "path": "/abs"}}}`):                                                                                                   "relative",
		screen(`{"data": {"route": {"source": "plugin", "path": "x"}}}`):                                                                                                  "free data name",
		screen(`{"redirect": "/runs", "blocks": [{"id": "a", "component": "core/a"}]}`):                                                                                   "redirect shows nothing",
		`{"apiVersion": "ui.arkhitekton/v1", "kind": "Navigation", "metadata": {"name": "n"}, "spec": {"items": [{"id": "a", "label": "A", "to": "relative"}]}}`:          "items[0].to",
		`{"apiVersion": "ui.arkhitekton/v1", "kind": "Widget", "metadata": {"name": "w"}, "spec": {"widget": {"action": {"label": "a", "method": "POST", "path": "x"}}}}`: "no action",
	} {
		_, err := Decode([]byte(raw))
		if err == nil || !strings.Contains(err.Error(), expected) {
			t.Errorf("%s: %v, expected a mention of %q", raw, err, expected)
		}
	}
}

func TestNamesSayTheirOwner(t *testing.T) {
	for name, owner := range map[string]string{"mse.environments": "mse", "release": "release", "core.header": "core"} {
		if got := Owner(name); got != owner {
			t.Errorf("%s: %s", name, got)
		}
	}
	if ComponentOwner("mse/deployed-badges") != "mse" {
		t.Error("component owner")
	}
}
