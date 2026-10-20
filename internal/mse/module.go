package mse

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"k8s.io/client-go/rest"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/module"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/flux"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/portaldata"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/storage"
)

// Module observes the environments of the projects the MSE plugin is switched on for.
//
// Run by the module supervisor: switched off, nothing touches Flux nor the portal's CRD, and the
// worker runs on a cluster carrying neither; switched on or edited, it starts again with the new
// settings, without a restart of the worker.
type Module struct {
	store     *storage.Store
	cluster   *rest.Config
	pipelines *catalog.Catalog
	// namespace is Flux's when neither the project nor the instance names one.
	namespace string
	logger    *slog.Logger
}

func NewModule(
	environments *storage.Store, cluster *rest.Config, pipelines *catalog.Catalog,
	namespace string, logger *slog.Logger,
) *Module {
	return &Module{store: environments, cluster: cluster, pipelines: pipelines, namespace: namespace, logger: logger}
}

var _ module.Module = (*Module)(nil)

func (m *Module) Plugin() string { return PluginID }

// Run synchronises the declared environments, then observes them until the context ends.
func (m *Module) Run(ctx context.Context, state module.State) error {
	// Two distinct dependencies, and two distinct clients: one says what the deployment applied,
	// the other what the application reports about itself. It is their gap that gives the
	// environments screen its value, and merging them would make it disappear.
	//
	// The attestation is **optional**: a cluster without the portal's CRD is a valid
	// configuration, and the dashboard falls back there to what the deployment declares.
	var attester Attester
	switch portalClient, err := portaldata.Open(m.cluster); {
	case errors.Is(err, portaldata.ErrAbsent):
		m.logger.Info("no attestation source in this cluster",
			"consequence", "the environments' state will be the one the deployer declares")
	case err != nil:
		return err
	default:
		attester = portalClient
		// `api_version` and not `version`: the logger already carries a `version` attribute — the
		// binary's — and two keys of the same name in one record make it unreadable.
		m.logger.Info("attestation source", "api_version", portalClient.Version())
	}
	observer := NewObserver(m.store, attester, m.logger)

	declared, err := m.declared(state)
	if err != nil {
		return err
	}

	// The guard rail: a promotion suffix matching no ImagePolicy would produce a valid tag, pushed
	// with no error, which Flux would ignore. Better not to observe.
	syncCtx, cancelSync := context.WithTimeout(ctx, 30*time.Second)
	err = observer.SyncEnvironments(syncCtx, declared)
	cancelSync()
	if err != nil {
		return err
	}
	return observer.Run(ctx)
}

// declared composes each project's environments and the Flux that observes them: the project's
// namespace, failing that the instance's, failing that the deployment's.
func (m *Module) declared(state module.State) ([]ProjectEnvironments, error) {
	// One client per distinct namespace, shared by the projects that share it: the client discovers
	// the API versions served, and doing it once per project would multiply the calls for the same
	// answer.
	deployers := map[string]*flux.Deployer{}
	enabled := map[string]bool{}
	var declared []ProjectEnvironments
	var slugs []string
	for _, entry := range state.Projects {
		project := entry.Project
		enabled[project.Slug] = true
		slugs = append(slugs, project.Slug)

		namespace := strings.TrimSpace(entry.Settings["namespace"])
		if namespace == "" {
			namespace = m.namespace
		}
		deployer, opened := deployers[namespace]
		if !opened {
			// The Flux client discovers the API versions served instead of hard-coding them: one
			// installation may serve `kustomize` and `image` in different versions.
			fluxClient, err := flux.Open(m.cluster, namespace)
			if err != nil {
				return nil, fmt.Errorf("Flux client of project %s: %w", project.Slug, err)
			}
			m.logger.Info("Flux client", "namespace", namespace, "versions", fluxClient.Versions())
			deployer = flux.NewDeployer(fluxClient, flux.DefaultPromotionKind, "")
			deployers[namespace] = deployer
		}

		own := m.pipelines.EnvironmentsOf(project.Slug)
		if len(own) == 0 {
			m.logger.Warn("project of the MSE module with no declared environment",
				"project", project.Slug, "consequence", "neither promotion nor observation for it")
		}
		declared = append(declared, ProjectEnvironments{
			ProjectID: project.ID, Slug: project.Slug, Environments: own, Deployer: deployer,
		})
		m.logger.Info("project of the MSE module", "project", project.Slug, "flux_namespace", namespace,
			"environments", len(own),
			// What its pipelines will clone: a repository that does not match what one believes one is
			// driving is otherwise visible only when the clone fails.
			"kustom_repo", entry.Settings["kustomRepoUrl"],
			"kustom_revision", entry.Settings["kustomRevision"],
			"kustom_overlay", entry.Settings["overlayLocation"])
	}
	for slug := range m.pipelines.Projects {
		if !enabled[slug] {
			// A section for a project that is absent or has switched the module off: without effect, and
			// saying so avoids hunting for why nothing appears.
			m.logger.Warn("environments declared for a project without the MSE module",
				"project", slug, "consequence", "ignored")
		}
	}
	m.logger.Info("projects of the MSE module", "projects", slugs)
	return declared, nil
}
