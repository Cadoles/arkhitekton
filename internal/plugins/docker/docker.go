// Package docker brings the pipelines of an image: building one, re-tagging one, scanning one.
//
// A project that publishes no image switches it off: it builds with another plugin, or releases
// tags.
package docker

import (
	"embed"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// ID is the plugin's identifier.
const ID = "docker"

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

// Plugin describes the Docker plugin.
func Plugin() capability.Plugin {
	all := Manifests()
	return capability.Plugin{
		ID:         ID,
		Version:    "1.0.0",
		APIVersion: sdk.APIVersion,
		Label:      "Docker",
		Summary:    "Construction, re-tag et scan de vulnérabilités (Trivy) des images de chaque projet.",
		PerProject: true,
		Global: []capability.Field{
			{
				Name: FieldRegistry, Label: "Registre", Type: capability.FieldText,
				Help: "Hôte et projet où les images sont publiées, sans nom d'image. " +
					"Vide : celui du déploiement (REGISTRY_PROJECT).",
				Placeholder: "reg.example.fr/my-team",
			},
		},
		Project: []capability.Field{
			{
				Name: FieldRegistry, Label: "Registre", Type: capability.FieldText,
				Help:        "Vide : celui de l'instance.",
				Placeholder: "reg.example.fr/my-team",
			},
			{
				Name: FieldRegistrySecret, Label: "Secret du registre", Type: capability.FieldText,
				Help: "Nom du Secret Kubernetes qui autorise à publier dans ce registre, créé " +
					"à côté des pipelines. Vide : celui du déploiement.",
				Placeholder: "regcred-my-project",
			},
			{
				Name: FieldDockerfile, Label: "Dockerfile", Type: capability.FieldText,
				Help: "Depuis la racine du dépôt. Une étape de construction le cite par " +
					"`{docker.dockerfile}`. Vide : `Dockerfile`.",
				Placeholder: DefaultDockerfile,
			},
			{
				Name: FieldBuildContext, Label: "Contexte de construction", Type: capability.FieldText,
				Help: "Répertoire envoyé au moteur de build. Une étape le cite par " +
					"`{docker.buildContext}`. Vide : la racine.",
				Placeholder: DefaultBuildContext,
			},
		},
		DefaultBuild: defaultBuild,
		Pipelines: []capability.Pipeline{
			build(pick(all, "build-image", "generate-image-tag", "buildx-build-image", "push-image")),
			retag(pick(all, "retag-image", "tag-image")),
			scan(pick(all, "docker-scan", "trivy-image")),
		},
	}
}

// The settings' names, as a project's configuration and a plan's variables `{docker.<name>}` say them.
const (
	FieldRegistry       = "registry"
	FieldRegistrySecret = "registrySecret"
	FieldDockerfile     = "dockerfile"
	FieldBuildContext   = "buildContext"
)

// The defaults of a project that names no Dockerfile and no context.
const (
	DefaultDockerfile   = "Dockerfile"
	DefaultBuildContext = "."
)

// Settings are a project's Docker settings, resolved.
type Settings struct {
	// Registry is where its images are published, `host/project`, without an image name.
	Registry string
	// RegistrySecret names the Secret allowing pushes to Registry. Empty: the deployment's.
	RegistrySecret string
	Dockerfile     string
	BuildContext   string
}

// Resolve reads a project's resolved plugin configuration, the instance's under the project's.
//
// The deployment's registry stands in for an instance that names none: it is where every image
// went before the setting existed.
func Resolve(config map[string]string, deploymentRegistry string) Settings {
	value := func(name, fallback string) string {
		if typed := strings.TrimSpace(config[name]); typed != "" {
			return typed
		}
		return fallback
	}
	return Settings{
		Registry:       strings.Trim(value(FieldRegistry, deploymentRegistry), "/"),
		RegistrySecret: value(FieldRegistrySecret, ""),
		Dockerfile:     value(FieldDockerfile, DefaultDockerfile),
		BuildContext:   value(FieldBuildContext, DefaultBuildContext),
	}
}

// defaultBuild is one image named after the project, from the Dockerfile its settings name. A
// project naming none is driven without being built, a repository of kustomizations: a build step
// would fail at the first `docker build`.
func defaultBuild(slug string, settings map[string]string) []capability.DefaultStep {
	if strings.TrimSpace(settings[FieldDockerfile]) == "" {
		return nil
	}
	return []capability.DefaultStep{{
		Pipeline: "build", Label: slug,
		Params: map[string]string{
			"image-name": slug,
			"dockerfile": "{" + ID + "." + FieldDockerfile + "}",
			"context":    "{" + ID + "." + FieldBuildContext + "}",
		},
	}}
}

func pick(all []capability.Manifest, names ...string) []capability.Manifest {
	var picked []capability.Manifest
	for _, manifest := range all {
		for _, name := range names {
			if manifest.Name == name {
				picked = append(picked, manifest)
			}
		}
	}
	return picked
}

func build(manifests []capability.Manifest) capability.Pipeline {
	return capability.Pipeline{
		Name:  "build",
		Label: "Construction d'image",
		Spec: capability.PipelineSpec{
			PipelineRef:   "build-image",
			Manifests:     manifests,
			ExternalTasks: []string{"git-clone"},
			Produces:      []capability.Product{capability.ProductImage},
			// A final release re-tags its candidate's images rather than building them again.
			Promote: &capability.Promotion{
				Pipeline: "retag",
				Params:   map[string]string{"image-name": "{artifact.name}"},
			},
			Params: map[string]capability.Param{
				"repo-url":          {Source: capability.ParamBinding, DefaultKey: "project.repoUrl"},
				"reg-project":       {Source: capability.ParamBinding, DefaultKey: "registry.project"},
				"insecure-registry": {Source: capability.ParamBinding, DefaultKey: "registry.insecure"},
				"dockerfile": {
					Source: capability.ParamInput, Label: "Dockerfile",
					Help: "Chemin du fichier qui construit cette image, depuis la racine du dépôt. " +
						"Dans un plan, `{docker.dockerfile}` pour celui du projet.",
					Placeholder: "./Dockerfile",
				},
				"context": {
					Source: capability.ParamInput, Label: "Contexte de construction",
					Help:        "Répertoire envoyé au moteur de build. Presque toujours la racine.",
					Placeholder: ".",
				},
				"revision": {Source: capability.ParamInput, Required: true, Suggest: "git-ref"},
				"version":  {Source: capability.ParamInput, Required: true, Suggest: "release-tag"},
				"tag-name": {Source: capability.ParamInput, Required: true, Suggest: "release-tag"},
				"image-name": {
					Source: capability.ParamInput, Required: true, Suggest: "image-name",
					Label: "Dépôt publié",
					Help: "Nom court du dépôt d'image, sans le registre ni le tag. C'est aussi le " +
						"nom sous lequel les étapes suivantes désigneront ce qu'il produit.",
					Placeholder: "portal",
				},
				"target": {
					Source: capability.ParamInput, Suggest: "build-target",
					Label: "Cible du Dockerfile",
					Help: "Étape `AS <nom>` qui produit cette image. Sur un Dockerfile sans " +
						"étapes, la dernière.",
					Placeholder: "web",
				},
			},
			Workspaces: []capability.Workspace{
				{Name: "git-credentials", Need: capability.NeedSourceRead},
				{Name: "dockerconfig", Need: capability.NeedRegistryPush},
				{Name: "shared-data", Volume: &capability.Volume{Storage: "2Gi"}},
			},
			Needs: []capability.Need{
				{Name: capability.NeedSourceRead, Reason: "clone the repository to build"},
				{Name: capability.NeedRegistryPush, Reason: "publish the image"},
			},
		},
	}
}

func retag(manifests []capability.Manifest) capability.Pipeline {
	return capability.Pipeline{
		Name:  "retag",
		Label: "Re-tag d'image",
		Spec: capability.PipelineSpec{
			PipelineRef: "retag-image",
			Manifests:   manifests,
			Produces:    []capability.Product{capability.ProductImage},
			Exclusive:   true,
			Params: map[string]capability.Param{
				"reg-project":       {Source: capability.ParamBinding, DefaultKey: "registry.project"},
				"insecure-registry": {Source: capability.ParamBinding, DefaultKey: "registry.insecure"},
				"image-name":        {Source: capability.ParamInput, Required: true, Suggest: "image-name"},
				"old-tag":           {Source: capability.ParamInput, Required: true, Suggest: "source-tag"},
				"new-tag":           {Source: capability.ParamInput, Required: true, Suggest: "release-tag"},
			},
			Workspaces: []capability.Workspace{
				{Name: "dockerconfig", Need: capability.NeedRegistryPush},
			},
			Needs: []capability.Need{
				{Name: capability.NeedRegistryPush, Reason: "set a tag on a published image"},
			},
		},
	}
}

// scan is declared in a plan, never chained on its own: it names the image a step built, which a
// default automation cannot know.
func scan(manifests []capability.Manifest) capability.Pipeline {
	return capability.Pipeline{
		Name:        "scan",
		Label:       "Scan d'image",
		Interpreter: scanned{},
		Spec: capability.PipelineSpec{
			PipelineRef:   "docker-scan",
			Group:         "security",
			Manifests:     manifests,
			ExternalTasks: []string{"publish-artifact"},
			Produces:      []capability.Product{capability.ProductResults},
			Params: map[string]capability.Param{
				"insecure-registry": {Source: capability.ParamBinding, DefaultKey: "registry.insecure"},
				"image": {
					Source: capability.ParamInput, Required: true, Label: "Image",
					Help: "L'image à scanner, avec son registre et son tag. Dans un plan, " +
						"`{artifacts.<étape>.image}` pour celle qu'une étape a construite.",
				},
			},
			Workspaces: []capability.Workspace{
				{Name: "dockerconfig", Need: capability.NeedRegistryPull},
				{Name: "shared-data", Volume: &capability.Volume{Storage: "1Gi"}},
			},
			Needs: []capability.Need{{Name: capability.NeedRegistryPull, Reason: "read the image to scan"}},
			Artifacts: []capability.Artifact{{
				Name: "results", Param: "results-upload-url", Object: "scan.tar.gz",
				Format:  capability.FormatArchive,
				Members: []capability.Member{{Match: "trivy/*.json", Parser: parse.Raw, Tool: trivyTool}},
			}},
		},
	}
}
