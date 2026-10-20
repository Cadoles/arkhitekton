// Package redmine reads versions, issues and progress from a Redmine instance.
package redmine

import (
	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// ID is the plugin's identifier.
const ID = "redmine"

// Plugin describes the plugin.
func Plugin() capability.Plugin {
	return capability.Plugin{
		ID:           ID,
		Version:      "1.0.0",
		APIVersion:   sdk.APIVersion,
		Capabilities: []capability.Header{{Kind: capability.KindTracker, Name: "tracker", Label: "Suivi"}},
		Label:        "Redmine",
		Summary:      "Versions, demandes et avancement lus dans une instance Redmine.",
		// One plugin-specific fact only. The probe observes two, but the new
		// version is a core fact — `version.created`, unprefixed — because every
		// tracker writes versions to the same table, so a reaction on it need not
		// be rewritten when the tracker changes.
		//
		// A closed issue stays here: it has no core equivalent, and it is an
		// event — something that happened once. A status change is not: it
		// happens ten times, and one fact per change would turn the feed into a
		// keystroke log.
		Events: []capability.EventSpec{
			{
				Suffix: "issue.closed",
				Label:  "Demande close",
				Payload: []capability.Field{
					{Name: "issue_id", Label: "Numéro"},
					{Name: "subject", Label: "Sujet"},
					{Name: "tracker", Label: "Type"},
					{Name: "status", Label: "Statut"},
					{Name: "version_id", Label: "Version visée"},
				},
			},
		},
		// The connection is declared at both scopes: globally as the default,
		// per project so a second tenant can have its own server without the
		// first having to type anything.
		Global: []capability.Field{
			{
				Name: "url", Label: "URL de l'instance", Type: capability.FieldURL,
				Help:        "Racine de Redmine, sans chemin.",
				Placeholder: "https://redmine.example.fr",
			},
			{
				Name: "api_key", Label: "Clé d'API", Type: capability.FieldText, Secret: true,
				Help: "Lecture seule suffit : le dashboard consulte, il n'écrit " +
					"jamais dans le suivi. La clé est scellée en base et n'est " +
					"jamais réaffichée.",
			},
		},
		Project: []capability.Field{
			{
				Name: "project", Label: "Identifiant du projet", Type: capability.FieldText,
				Required: true,
				Help: "Tel qu'il apparaît dans l'URL Redmine, et non son " +
					"libellé.",
				Placeholder: "my-project",
			},
			{
				Name: "review_statuses", Label: "Statuts de revue", Type: capability.FieldList,
				Help: "Statuts qui rendent une demande éligible à la note de " +
					"release. Le vocabulaire appartient au client.",
				Placeholder: "En revue",
			},
			{
				Name: "url", Label: "URL de l'instance", Type: capability.FieldURL,
				Help:        "Vide : celle de l'instance s'applique.",
				Placeholder: "https://redmine.example.fr",
			},
			{
				Name: "api_key", Label: "Clé d'API", Type: capability.FieldText, Secret: true,
				Help: "Vide : celle de l'instance s'applique.",
			},
		},
		PerProject: true,
	}
}
