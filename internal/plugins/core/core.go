// Package core brings the pipelines every project uses: building an image and re-tagging one.
//
// Their Tekton definitions arrive with the dashboard's own kustomization, like the migration Job,
// so the capabilities ship no manifests.
package core

import "forge.cadoles.com/vcarroy/arkhitekton/internal/capability"

// ID is the plugin's identifier.
const ID = "core"

// Plugin describes the core plugin.
func Plugin() capability.Plugin {
	return capability.Plugin{ID: ID, Version: "1.0.0", Pipelines: []capability.Pipeline{build(), retag()}}
}

func build() capability.Pipeline {
	return capability.Pipeline{
		Name:  "build",
		Label: "Construction d'image",
		Spec: capability.PipelineSpec{
			PipelineRef: "build-image",
			Produces:    []capability.Product{capability.ProductImage},
			Params: map[string]capability.Param{
				"repo-url":    {Source: capability.ParamBinding, DefaultKey: "project.repoUrl"},
				"reg-project": {Source: capability.ParamBinding, DefaultKey: "registry.project"},
				"dockerfile": {
					Source: capability.ParamInput, Label: "Dockerfile",
					Help: "Chemin du fichier qui construit cette image, depuis la racine du dépôt. " +
						"Vide : celui du projet.",
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
					Source: capability.ParamInput, Required: true, Suggest: "build-target",
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

func retag() capability.Pipeline {
	return capability.Pipeline{
		Name:  "retag",
		Label: "Re-tag d'image",
		Spec: capability.PipelineSpec{
			PipelineRef: "retag-image",
			Produces:    []capability.Product{capability.ProductImage},
			Exclusive:   true,
			Params: map[string]capability.Param{
				"reg-project": {Source: capability.ParamBinding, DefaultKey: "registry.project"},
				"image-name":  {Source: capability.ParamInput, Required: true, Suggest: "image-name"},
				"old-tag":     {Source: capability.ParamInput, Required: true, Suggest: "release-tag"},
				"new-tag":     {Source: capability.ParamInput, Required: true, Suggest: "release-tag"},
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
