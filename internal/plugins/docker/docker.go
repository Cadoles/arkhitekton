// Package docker brings the pipelines of an image: building one and re-tagging one.
//
// The dashboard needs them to work: its releases are images, and a final release re-tags its
// pre-release. The plugin therefore cannot be switched off.
package docker

import (
	"embed"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
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
		Summary:    "Construction et re-tag des images de chaque projet.",
		Mandatory:  true,
		Pipelines: []capability.Pipeline{
			build(pick(all, "build-image", "generate-image-tag", "buildx-build-image", "push-image")),
			retag(pick(all, "retag-image", "tag-image")),
		},
	}
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
			Params: map[string]capability.Param{
				"repo-url":          {Source: capability.ParamBinding, DefaultKey: "project.repoUrl"},
				"reg-project":       {Source: capability.ParamBinding, DefaultKey: "registry.project"},
				"insecure-registry": {Source: capability.ParamBinding, DefaultKey: "registry.insecure"},
				"dockerfile": {
					Source: capability.ParamInput, Label: "Dockerfile",
					Help: "Chemin du fichier qui construit cette image, depuis la racine du dépôt. " +
						"Dans un plan, `{project.dockerfile}` pour celui du projet.",
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
				"old-tag":           {Source: capability.ParamInput, Required: true, Suggest: "release-tag"},
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
