// Package review brings an AI review of a pull request, written by Conclave: several coding agents
// read the request at its head, and a lead agent checks their findings against the code and merges
// them into one review with a verdict.
//
// The models are the operator's, in the plugin's settings, the instance's with a project's over
// them: the plugin ships the pipeline, never a model nor a provider. The provider's key is in a
// Secret the review mounts, never in a setting. The review reads the forge with a token of its own,
// which reads and never writes; what is published on the pull request is the core's action, after
// the review.
package review

import (
	"embed"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// ID is the plugin's identifier.
const ID = "review"

// Group is where a review's findings are shown: apart from the security audit, and out of the
// score, since a reviewer's remark is an opinion to weigh, not a measure.
const Group = "review"

//go:embed manifests
var manifests embed.FS

// Manifests are the Tekton definitions the plugin ships.
func Manifests() []capability.Manifest {
	loaded, err := capability.LoadManifests(manifests, "manifests")
	if err != nil {
		// The embedding is fixed at compile time; a test reads it.
		panic(err)
	}
	return loaded
}

// Plugin describes the review plugin.
func Plugin() capability.Plugin {
	return capability.Plugin{
		ID:         ID,
		Version:    "1.0.0",
		APIVersion: sdk.APIVersion,
		Label:      "Revue IA",
		Summary:    "Revue d'une pull request par plusieurs agents IA (Conclave), sur les modèles que l'opérateur configure.",
		PerProject: true,
		Global:     settings,
		Project:    settings,
		Pipelines:  []capability.Pipeline{pullRequest(Manifests())},
	}
}

// settings are Conclave's, all of them overridable by a project: a project may review with other
// models, on another endpoint, or exclude its generated files.
var settings = []capability.Field{
	{Name: "endpoint", Label: "Point d'accès des modèles", Type: capability.FieldURL,
		Help:        "L'adresse d'une API compatible OpenAI (vLLM, Ollama, LiteLLM…), le code relu y part.",
		Placeholder: "https://llm.example.fr/v1"},
	// Sealed, and never a pipeline's parameter: the worker writes it into the project's Secret
	// right before a review, which mounts it through `project.reviewSecret`.
	{Name: "apiKey", Label: "Clé d'API", Type: capability.FieldText, Secret: true,
		Help: "La clé du point d'accès, vide s'il n'en demande pas. Scellée, jamais réaffichée, et recopiée avant chaque revue dans un secret du projet que le pipeline monte."},
	{Name: "api", Label: "API", Type: capability.FieldText,
		Help: "Le protocole que Pi parle à ce point d'accès. Vide : openai-completions.", Placeholder: "openai-completions"},
	{Name: "reviewers", Label: "Modèles des relecteurs", Type: capability.FieldList,
		Help:        "Un relecteur par modèle, séparés par des virgules. Des modèles différents relisent mieux qu'un seul plusieurs fois.",
		Placeholder: "qwen3-coder,devstral"},
	{Name: "lead", Label: "Modèle du lead", Type: capability.FieldText,
		Help: "Celui qui vérifie les remarques des relecteurs dans le code et écrit la revue.", Placeholder: "qwen3-coder"},
	{Name: "thinking", Label: "Réflexion", Type: capability.FieldText,
		Help: "Le niveau de réflexion des agents : off, minimal, low, medium, high. Vide : celui du modèle.", Placeholder: "medium"},
	{Name: "timeout", Label: "Durée de la revue", Type: capability.FieldText,
		Help: "Toute la revue, relecteurs et lead. Vide : 30m.", Placeholder: "30m"},
	{Name: "agentTimeout", Label: "Durée d'un relecteur", Type: capability.FieldText, Help: "Vide : 15m.", Placeholder: "15m"},
	{Name: "leadTimeout", Label: "Durée du lead", Type: capability.FieldText, Help: "Vide : 10m.", Placeholder: "10m"},
	{Name: "maxParallel", Label: "Relecteurs en parallèle", Type: capability.FieldText, Help: "Vide : 2.", Placeholder: "2"},
	{Name: "diffExclude", Label: "Exclus du diff", Type: capability.FieldList,
		Help:        "Des motifs de chemins retirés du diff donné aux relecteurs, qui gardent les fichiers sous la main : ce qui est généré.",
		Placeholder: "composer.lock,package-lock.json,*.min.js"},
	{Name: "ci", Label: "La CI pèse sur le verdict", Type: capability.FieldBool,
		Help: "Cochée, un check en échec sur la tête fait demander des modifications, un check en cours empêche d'approuver."},
	{Name: "image", Label: "Image", Type: capability.FieldText,
		Help:        "L'image qui porte Conclave et Pi (images/review du dépôt d'arkhitekton).",
		Placeholder: "reg.example.fr/arkhitekton-review:v1"},
}

// setting binds a parameter to the plugin's setting of that name.
func setting(name string, required bool) capability.Param {
	return capability.Param{Source: capability.ParamBinding, DefaultKey: ID + "." + name, Required: required}
}

func pullRequest(manifests []capability.Manifest) capability.Pipeline {
	return capability.Pipeline{
		Name:        "pull-request",
		Label:       "Revue IA d'une pull request",
		Interpreter: reviews{},
		Spec: capability.PipelineSpec{
			PipelineRef:   "review-pull-request",
			Group:         Group,
			Manifests:     manifests,
			ExternalTasks: []string{"publish-artifact"},
			Params: map[string]capability.Param{
				"repo-url":      {Source: capability.ParamBinding, DefaultKey: "project.repoUrl"},
				"forge-url":     {Source: capability.ParamBinding, DefaultKey: "project.forgeUrl", Required: true},
				"image":         setting("image", true),
				"endpoint":      setting("endpoint", true),
				"api":           setting("api", false),
				"reviewers":     setting("reviewers", true),
				"lead":          setting("lead", true),
				"thinking":      setting("thinking", false),
				"timeout":       setting("timeout", false),
				"agent-timeout": setting("agentTimeout", false),
				"lead-timeout":  setting("leadTimeout", false),
				"max-parallel":  setting("maxParallel", false),
				"diff-exclude":  setting("diffExclude", false),
				"ci":            setting("ci", false),
				// Filled by the run: from a pull request's page, or by a reaction to its facts.
				"number": {Source: capability.ParamInput, Required: true, Suggest: "pull-request", Label: "Pull request"},
			},
			Workspaces: []capability.Workspace{
				{Name: "git-credentials", Need: capability.NeedSourceRead},
				{Name: "model-access", Need: capability.NeedModelAccess},
				{Name: "forge-token", Need: capability.NeedForgeRead},
				{Name: "shared-data", Volume: &capability.Volume{Storage: "2Gi"}},
			},
			Needs: []capability.Need{
				{Name: capability.NeedSourceRead, Reason: "clone the repository and fetch the pull request's head"},
				{Name: capability.NeedModelAccess, Reason: "ask the reviewers' models, with the key under `api-key`: bind `project.reviewSecret`, which the plugin's setting feeds, or a Secret of yours carrying Pi's whole `models.json`"},
				{Name: capability.NeedForgeRead, Reason: "read the pull request, its discussion, the issues it cites and its checks, under the key `token`"},
			},
			Produces:  []capability.Product{capability.ProductResults},
			Exclusive: true,
			Artifacts: []capability.Artifact{{
				Name: "results", Param: "results-upload-url", Object: "review.tar.gz",
				Format: capability.FormatArchive,
				Members: []capability.Member{
					{Match: "review/*.json", Parser: parse.Raw, Tool: "conclave"},
					{Match: "review/*.md", Parser: parse.Attach, Tool: "conclave", MediaType: "text/markdown"},
				},
			}},
		},
	}
}
