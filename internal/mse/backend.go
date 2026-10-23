package mse

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"forge.cadoles.com/vcarroy/arkhitekton/host"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/storage"
)

// Backend brings together the two stores the plugin's routes need: the core's for the
// releases, the projects, the queue and the audit, the plugin's for its own tables.
type Backend struct {
	Core
	*storage.Store
}

// Core is the dashboard's store, embedded under a name of its own: the plugin's is `Store`.
type Core interface{ host.Store }

func NewBackend(core host.Store, pool *pgxpool.Pool) Backend {
	return Backend{Core: core, Store: storage.New(pool)}
}

// Deployments returns, per release tag, the environments running it.
//
// The core displays the "deployed on" badge without reading the plugin's tables: it calls this
// function, which returns nothing when the module is switched off for the project.
func Deployments(environments *storage.Store, modules *host.Modules) func(context.Context, int64) (map[string][]string, error) {
	return func(ctx context.Context, projectID int64) (map[string][]string, error) {
		if !modules.EnabledIn(ctx, projectID, PluginID) {
			return nil, nil
		}
		return environments.DeployedByRelease(ctx, projectID)
	}
}

// Promotions returns a release's requested promotions, in the core's vocabulary. Nothing when
// the module is switched off.
func Promotions(
	environments *storage.Store, modules *host.Modules,
) func(context.Context, int64, int64) ([]host.ReleasePromotion, error) {
	return func(ctx context.Context, projectID, releaseID int64) ([]host.ReleasePromotion, error) {
		if !modules.EnabledIn(ctx, projectID, PluginID) {
			return nil, nil
		}
		promotions, err := environments.ListPromotions(ctx, releaseID)
		if err != nil {
			return nil, err
		}
		converted := make([]host.ReleasePromotion, 0, len(promotions))
		for _, promotion := range promotions {
			converted = append(converted, host.ReleasePromotion{
				Environment: promotion.EnvironmentID, Image: promotion.ImageName,
				Tag: promotion.Tag, Digest: promotion.Digest,
				JobID: promotion.JobID, PushedAt: promotion.PushedAt,
			})
		}
		return converted, nil
	}
}
