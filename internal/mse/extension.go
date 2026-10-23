package mse

import (
	"context"
	"os"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/host"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/flux"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/storage"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// Extension is the MSE plugin as a distribution of arkhitekton compiles it in: its pipelines, its
// integration tests added to the `php` stack, its tables, its routes and its observation loop.
func Extension() host.Extension {
	return host.Extension{
		ID:         PluginID,
		Plugin:     plugin.Plugin(),
		Stacks:     map[string][]capability.Manifest{"php": plugin.Manifests()},
		Migrations: &storage.Migrations,
		Seed:       host.Seed{Settings: seedSettings, Projects: seedProjects},
		API:        api,
		Worker:     worker,
	}
}

// api mounts the plugin's routes and gives the core its deployments, promotions, environments and
// scheme check. The routes are always mounted, and refused per route when the module is switched
// off. The deployer has no client: planning a promotion, or asking "can you route this scheme?",
// does not need the cluster.
func api(lent host.APIHost) (host.APIHooks, error) {
	backend := NewBackend(lent.Store, lent.Pool)
	deployer := flux.NewDeployer(nil, flux.DefaultPromotionKind, "")
	return host.APIHooks{
		Routes: []host.Mount{
			NewHandler(backend, lent.Catalog, lent.Authorizer, deployer, lent.Logger).Routes,
			NewSignoffsHandler(backend, lent.Logger).Routes,
		},
		Deployments:            Deployments(backend.Store, lent.Modules),
		Promotions:             Promotions(backend.Store, lent.Modules),
		EnvironmentSuggestions: EnvironmentSuggestions(lent.Catalog, lent.Modules, lent.Store),
		// Asked only when the plugin is switched on: without it, nothing promotes, and no scheme has
		// to be refused.
		SchemeCheck: func(ctx context.Context, scheme host.Scheme) error {
			if !lent.Modules.Enabled(ctx, PluginID) {
				return nil
			}
			return deployer.CheckScheme(scheme)
		},
	}, nil
}

// worker runs the observation of the environments, states the fact of a promotion, and adds the
// plugin's settings to a project's pipelines.
func worker(lent host.WorkerHost) (host.WorkerHooks, error) {
	return host.WorkerHooks{
		Modules: []host.Module{
			NewModule(storage.New(lent.Pool), lent.Cluster, lent.Catalog, fluxNamespace(), lent.Logger),
		},
		PromotionFacts:  Facts{},
		ProjectSettings: PipelineSettings(lent.Store),
	}, nil
}

// fluxNamespace is Flux's when neither a project nor the instance names one.
func fluxNamespace() string { return envOr("FLUX_NAMESPACE", "flux-system") }

// seedSettings returns the plugin's settings the environment supplies.
//
// A **seeding**, and not a read: these values set the first setting, then the database is
// authoritative.
func seedSettings() map[string]string {
	settings := PipelineDefaults(
		envOr("FORGE_MSE_KUSTOM_REPO_URL", "ssh://git@forge.cadoles.com:2222/CNOUS/mse-kustom.git"),
		// The branch of the deployment infrastructure to audit. Without it, the audit fell back to
		// the pipeline's default, `unstable`, `CNOUS/mse-kustom`'s convention, and failed at the clone
		// on any repository that does not carry it: "couldn't find remote ref unstable".
		envOr("FORGE_MSE_KUSTOM_REVISION", "unstable"),
		envOr("FORGE_E2E_REPO_URL", "ssh://git@forge.cadoles.com:2222/CNOUS/mse-e2e.git"),
		envOr("MSE_OVERLAY_LOCATION", "overlays/pipeline"),
	)
	if namespace := strings.TrimSpace(os.Getenv("FLUX_NAMESPACE")); namespace != "" {
		settings["namespace"] = namespace
	}
	return settings
}

// seedProjects completes the projects a fresh instance is seeded with: the application's images,
// and the repositories the plugin's pipelines clone, the kustomization and the end-to-end
// campaign.
//
// The application's stages are named here, and nowhere else: they describe a given repository,
// not the product.
func seedProjects(seeded []host.Project) []host.Project {
	promoted, held := true, false
	targets := []host.Target{
		// The HTTP runtime: front office, back office, web services.
		{Stage: "web", Image: "portal", Promote: &promoted},
		// The Messenger consumers, the cronjobs and the jobs. Named `portal-consumer` until the
		// FrankenPHP migration, where it stopped serving the consumers only.
		{Stage: "cli", Image: "portal-cli", Promote: &promoted},
		// The "fat" image: xdebug, xhprof, PostgreSQL client. It runs the bootstrap job of the
		// fake-data overlays and allows reproducing a defect on exactly the version delivered. Built
		// but not promoted: no environment of the flow deploys it.
		{Stage: "dev", Image: "portal-dev", Promote: &held},
	}
	for index, project := range seeded {
		// The application is the built one.
		if project.Dockerfile != "" && len(project.Targets) == 0 {
			seeded[index].Targets, seeded[index].MultiStage = targets, true
		}
	}

	settings := seedSettings()
	for _, cloned := range []struct{ clone, label string }{
		{settings["kustomRepoUrl"], "Kustomizations"},
		{settings["e2eRepoUrl"], "Campagne E2E"},
	} {
		repository, forgeURL := host.RepositoryFromCloneURL(cloned.clone)
		if repository == "" {
			continue
		}
		seeded = append(seeded, host.Project{
			Repository: repository, Label: cloned.label, ForgeURL: forgeURL, BuildContext: ".",
			CreatedBy: "configuration",
		})
	}
	return seeded
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
