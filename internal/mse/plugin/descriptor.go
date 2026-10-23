package plugin

import (
	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// descriptor is what the plugin says of itself beside its pipelines: the module's settings and the
// fact it observes.
func descriptor() capability.Plugin {
	return capability.Plugin{
		ID:           ID,
		Version:      "1.0.0",
		APIVersion:   sdk.APIVersion,
		Capabilities: []capability.Header{{Kind: capability.KindModule, Name: "module", Label: "Module"}},
		Label:        "MSE",
		Summary:      "Environnements, promotion par Flux et attestation par PortalData.",
		Global: []capability.Field{
			{
				Name: "namespace", Label: "Namespace Flux", Type: capability.FieldText,
				Help:        "Là où vivent les objets Flux, distinct de celui des pipelines.",
				Placeholder: "flux-system",
			},
			{
				Name: "kustomRepoUrl", Label: "Dépôt de kustomisation", Type: capability.FieldText,
				Help:        "Le dépôt que les pipelines clonent pour déployer.",
				Placeholder: "ssh://git@forge.example:2222/org/kustom.git",
			},
			{
				Name: "kustomRevision", Label: "Révision de la kustomisation", Type: capability.FieldText,
				Help:        "La branche auditée et déployée.",
				Placeholder: "main",
			},
			{
				Name: "e2eRepoUrl", Label: "Dépôt des tests E2E", Type: capability.FieldText,
				Placeholder: "ssh://git@forge.example:2222/org/tests-e2e.git",
			},
			{
				Name: "overlayLocation", Label: "Emplacement de l'overlay", Type: capability.FieldText,
				Help: "Cherché dans la kustomisation, et non dans l'application : " +
					"viser la racine de l'une parce que l'autre y porte son Dockerfile " +
					"a coûté un déploiement.",
				Placeholder: "overlays/pipeline",
			},
		},
		// A project may deploy through a Flux other than the instance's, with its
		// ImagePolicy and Kustomization objects in another namespace. Empty
		// inherits.
		Project: []capability.Field{
			{
				Name: "namespace", Label: "Namespace Flux", Type: capability.FieldText,
				Help:        "Là où vivent les objets Flux de ce projet. Vide : celui de l'instance.",
				Placeholder: "flux-system",
			},
			{
				Name: "kustomRepoUrl", Label: "Dépôt de kustomisation", Type: capability.FieldText,
				Help:        "Le dépôt que les pipelines clonent pour déployer.",
				Placeholder: "ssh://git@forge.example:2222/org/kustom.git",
			},
			{
				Name: "kustomRevision", Label: "Révision de la kustomisation", Type: capability.FieldText,
				Help:        "La branche auditée et déployée.",
				Placeholder: "main",
			},
			{
				Name: "e2eRepoUrl", Label: "Dépôt des tests E2E", Type: capability.FieldText,
				Placeholder: "ssh://git@forge.example:2222/org/tests-e2e.git",
			},
			{
				Name: "overlayLocation", Label: "Emplacement de l'overlay", Type: capability.FieldText,
				Help: "Cherché dans la kustomisation, et non dans l'application : " +
					"viser la racine de l'une parce que l'autre y porte son Dockerfile " +
					"a coûté un déploiement.",
				Placeholder: "overlays/pipeline",
			},
		},
		// Promotion is a plugin fact, not a core one: the plugin defines what a
		// promotion is, so the plugin observes it.
		Events: []capability.EventSpec{
			{
				Suffix: "release.promoted",
				Label:  "Promotion aboutie",
				Payload: []capability.Field{
					{Name: "release_id", Label: "Release"},
					{Name: "tag", Label: "Tag"},
					{Name: "environment", Label: "Environnement"},
					{Name: "job_id", Label: "Job"},
				},
			},
		},
		// Per project: only a project that belongs to the MSE portal gets it. The
		// instance switch is both permission and default — switched off there, no
		// project can switch it back on.
		PerProject: true,
	}
}
