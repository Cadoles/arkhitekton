package plugin

import "testing"

// The integration tests deploy MSE: their definitions travel with this plugin, not with PHP's.
func TestThePluginShipsTheIntegrationTests(t *testing.T) {
	kinds := map[string]string{}
	for _, manifest := range Manifests() {
		kinds[manifest.Name] = manifest.Kind
	}
	if kinds["php-integration-test"] != "Pipeline" || kinds["integration-test"] != "Task" {
		t.Errorf("manifests = %v", kinds)
	}
}
