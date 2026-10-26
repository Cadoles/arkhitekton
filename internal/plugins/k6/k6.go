// Package k6 reads load campaigns.
//
// The dashboard does not launch them: a campaign's runner publishes its summaries itself, one per
// scenario, and the capability only reads the runs of the Pipeline it observes.
package k6

import (
	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// ID is the plugin's identifier.
const ID = "k6"

// Plugin describes the k6 plugin.
func Plugin() capability.Plugin {
	summaries := func(dir string) capability.Artifact {
		return capability.Artifact{
			Name: dir, Object: dir + "/*.json", Format: capability.FormatFile,
			Members: []capability.Member{{Match: "*.json", Parser: parse.K6SummaryJSON, Tool: "k6"}},
		}
	}
	return capability.Plugin{
		ID:         ID,
		Version:    "1.0.0",
		APIVersion: sdk.APIVersion,
		Label:      "k6",
		Summary:    "Campagnes de charge k6 et comparaison à une référence.",
		Pipelines: []capability.Pipeline{{
			Name:        "load",
			Label:       "Tests de charge",
			Interpreter: campaignReader{},
			Spec: capability.PipelineSpec{
				ObservedPipelines: []string{"load-test"},
				Group:             "load",
				Produces:          []capability.Product{capability.ProductResults},
				// `load/` per the contract, `k6/` from the tool's name: both are in use.
				Artifacts: []capability.Artifact{summaries("load"), summaries("k6")},
			},
		}}}
}
