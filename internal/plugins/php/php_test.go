package php

import "testing"

// The manifests are extracted from what actually runs. If one disappears from the embedding (a
// file renamed, a directory moved), nothing else would report it before a pipeline could not be
// found in the cluster.
func TestThePluginShipsItsPipelinesAndTasks(t *testing.T) {
	kinds := map[string]string{}
	for _, manifest := range Manifests() {
		kinds[manifest.Name] = manifest.Kind
	}
	for name, kind := range map[string]string{
		"php-static-tests": "Pipeline", "php-unit-tests": "Pipeline",
		"phpstan": "Task", "cs-fixer": "Task", "unit-test": "Task",
	} {
		if kinds[name] != kind {
			t.Errorf("%s: %q, expected a %s", name, kinds[name], kind)
		}
	}
}
