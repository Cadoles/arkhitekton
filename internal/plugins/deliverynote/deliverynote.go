// Package deliverynote declares the delivery note, the contractual deliverable summarising a
// release's scope and checks. The dashboard renders it.
package deliverynote

import (
	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// ID is the plugin's identifier.
const ID = "bon-de-livraison"

// Plugin describes the plugin.
func Plugin() capability.Plugin {
	return capability.Plugin{
		ID:           ID,
		Version:      "1.0.0",
		APIVersion:   sdk.APIVersion,
		Capabilities: []capability.Header{{Kind: capability.KindExporter, Name: "exporter", Label: "Livrables"}},
		Label:        "Bon de livraison",
		Summary:      "Livrable contractuel récapitulant le périmètre et les vérifications.",
		// Per project: it used to be offered on every release, including those of
		// projects that produce no such deliverable.
		PerProject: true,
	}
}
