// Package gitea offers a Gitea forge's branches, tags and pull requests to the dashboard, and
// writes back commit statuses and comments.
package gitea

import (
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// ID is the plugin's identifier.
const ID = "gitea"

// Plugin describes the plugin.
func Plugin() capability.Plugin {
	return capability.Plugin{
		ID:         ID,
		Version:    "1.1.0",
		APIVersion: sdk.APIVersion,
		Forge:      forge{},
		Probes:     []capability.Probe{pullProbe{}},
		Executor:   executor{},
		Webhook: &capability.Webhook{
			Secret: webhookSecret, Signature: "X-Gitea-Signature", Event: "X-Gitea-Event",
			Scheme: capability.SignatureHMACSHA256, Receiver: receiver{now: time.Now},
		},
		Label: "Gitea",
		Summary: "Branches, tags et références proposés au formulaire de release, " +
			"demandes de fusion ouvertes et poussées reçues de la forge, et retour vers la forge.",
		Actions: []capability.ActionSpec{
			{
				Suffix: "status", Label: "Statut de commit",
				Summary: "Pose un statut sur un commit, visible dans la forge à côté " +
					"de la demande de fusion.",
				Fields: []capability.ActionField{
					{
						Name: "commit", Label: "Commit", Required: true, Template: true,
						Help: "Le SHA visé. `{git_commit}` pour celui de la " +
							"construction qui vient de conclure.",
						Placeholder: "{git_commit}",
					},
					{
						Name: "state", Label: "État", Required: true,
						OneOf: []string{"pending", "success", "failure", "error"},
						Help: "Le vocabulaire de la forge, et il est fermé. Ce " +
							"n'est **pas** celui d'un fait : `{outcome}` rend " +
							"`succeeded`, que la forge accepte sans savoir " +
							"l'afficher. Pour refléter l'issue, écrire l'état en " +
							"dur et apparier la réaction sur `outcome`.",
						Placeholder: "success",
					},
					{
						Name: "context", Label: "Contexte", Required: true,
						Help: "Le nom sous lequel le statut apparaît. Deux gestes " +
							"partageant ce nom se remplacent : c'est ainsi qu'un " +
							"`pending` devient un `success`.",
						Placeholder: "arkhitekton/vérifications",
					},
					{
						Name: "description", Label: "Description", Template: true,
						Help: "Une ligne, avec des variables `{tag}`, `{outcome}`.",
					},
					{
						Name: "target_url", Label: "Lien", Template: true,
						Help: "Où mène le statut depuis la forge. Vide, il ne mène " +
							"nulle part et oblige à revenir ici à la main.",
					},
				},
			},
			{
				Suffix: "comment", Label: "Commentaire sur une demande de fusion",
				Summary: "Ajoute un commentaire à une demande de fusion. Le numéro " +
					"vient du fait qui l'a fait connaître.",
				Fields: []capability.ActionField{
					{
						Name: "number", Label: "Numéro", Required: true, Template: true,
						Help: "`{number}`, tel que le fait « demande de fusion " +
							"ouverte » le porte.",
						Placeholder: "{number}",
					},
					{
						Name: "body", Label: "Message", Required: true, Template: true,
						Help: "Markdown, avec les variables de la charge du fait.",
					},
				},
			},
		},
		Events: []capability.EventSpec{{
			Suffix: push,
			Label:  "Poussée",
			Payload: []capability.Field{
				{Name: "revision", Label: "Commit poussé"},
				{Name: "branch", Label: "Branche"},
				{Name: "tag", Label: "Tag"},
				{Name: "ref", Label: "Référence"},
				{Name: "before", Label: "Commit précédent"},
				{Name: "pusher", Label: "Auteur de la poussée"},
				{Name: "commits", Label: "Nombre de commits"},
				{Name: "message", Label: "Message du dernier commit"},
				{Name: "compare_url", Label: "Comparaison", Type: capability.FieldURL},
			},
		}, {
			Suffix: "pull_request.opened",
			Label:  "Demande de fusion ouverte",
			Payload: []capability.Field{
				{Name: "number", Label: "Numéro"},
				{Name: "title", Label: "Titre"},
				{Name: "author", Label: "Auteur"},
				{Name: "source_branch", Label: "Branche source"},
				{Name: "target_branch", Label: "Branche cible"},
				{Name: "url", Label: "Lien", Type: capability.FieldURL},
			},
		}},
		Global: []capability.Field{
			{
				Name: "url", Label: "URL de la forge", Type: capability.FieldURL,
				Placeholder: "https://forge.example.fr",
			},
			{
				Name: "token", Label: "Jeton", Type: capability.FieldText, Secret: true,
				Help: "Une portée `read:repository` suffit pour lire et observer, " +
					"et vaut mieux : un jeton complet ouvre la forge entière au " +
					"nom de son détenteur. `write:repository` n'est nécessaire " +
					"que pour poser des tags, des statuts ou des commentaires.",
			},
			webhookSecretField,
		},
		// The repository is not asked again here: it is already declared in the
		// project's own parameters, and two places to correct it would make one
		// place to forget. The webhook's secret is, since each repository's hook
		// may carry its own.
		Project: []capability.Field{webhookSecretField},
		//
		// Per-project activation, though: watching a repository's pull requests
		// is a project's decision, even when the connection is instance-wide.
		PerProject: true,
	}
}

// webhookSecretField holds the secret the forge's hook signs its deliveries with. At both scopes: one
// secret for every repository's hook, or one per repository. A hook is set per repository either
// way, since its address names the project.
var webhookSecretField = capability.Field{
	Name: webhookSecret, Label: "Secret du webhook", Type: capability.FieldText, Secret: true,
	Help: "Le secret saisi dans le webhook de la forge, type de contenu `application/json`, " +
		"adresse `<dashboard>/api/v1/hooks/gitea/projects/<projet>`. Sans secret, " +
		"les livraisons sont refusées.",
}
