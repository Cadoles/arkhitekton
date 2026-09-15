package mse

import (
	"context"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	dashhttp "forge.cadoles.com/vcarroy/mse-dashboard/internal/http"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plugin"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

type pluginSettings map[string]store.PluginSettings

func (p pluginSettings) PluginSettingsAll(context.Context) (map[string]store.PluginSettings, error) {
	return p, nil
}

func modulesWith(enabled bool) *dashhttp.Modules {
	return dashhttp.NewModules(pluginSettings{
		plugin.ModuleMSE: {PluginID: plugin.ModuleMSE, Enabled: enabled},
	}, quietLogger())
}

// Les environnements visibles sont proposés quand le module est allumé, et aucun
// quand il est éteint : le champ reste alors en saisie libre.
func TestLesEnvironnementsProposesSuiventLeModule(t *testing.T) {
	hidden := false
	environments := []catalog.Environment{
		{ID: "qualif", Label: "Qualification"},
		{ID: "fantome", Label: "Fantôme", Visible: &hidden},
	}

	proposed := EnvironmentSuggestions(environments, modulesWith(true))(context.Background())
	if len(proposed) != 1 || proposed[0].Value != "qualif" || proposed[0].Label != "Qualification" {
		t.Errorf("module allumé: %+v", proposed)
	}

	if proposed := EnvironmentSuggestions(environments, modulesWith(false))(context.Background()); proposed != nil {
		t.Errorf("module éteint: %+v, attendu aucune liste", proposed)
	}
}
