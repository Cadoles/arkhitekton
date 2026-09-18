package mse

import (
	"context"

	dashhttp "forge.cadoles.com/vcarroy/arkhitekton/internal/http"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/storage"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// Backend brings together the two stores the plugin's routes need: the core's for the
// releases, the projects, the queue and the audit, the plugin's for its own tables.
type Backend struct {
	*store.DB
	*storage.Store
}

func NewBackend(db *store.DB) Backend {
	return Backend{DB: db, Store: storage.New(db.Pool())}
}

// Deployments returns, per release tag, the environments running it.
//
// The core displays the "deployed on" badge without reading the plugin's tables: it calls this
// function, which returns nothing when the module is switched off for the project.
func Deployments(environments *storage.Store, modules *dashhttp.Modules) func(context.Context, int64) (map[string][]string, error) {
	return func(ctx context.Context, projectID int64) (map[string][]string, error) {
		if !modules.EnabledIn(ctx, projectID, plugin.ModuleMSE) {
			return nil, nil
		}
		return environments.DeployedByRelease(ctx, projectID)
	}
}

// Promotions returns a release's requested promotions, in the core's vocabulary. Nothing when
// the module is switched off.
func Promotions(
	environments *storage.Store, modules *dashhttp.Modules,
) func(context.Context, int64, int64) ([]dashhttp.ReleasePromotion, error) {
	return func(ctx context.Context, projectID, releaseID int64) ([]dashhttp.ReleasePromotion, error) {
		if !modules.EnabledIn(ctx, projectID, plugin.ModuleMSE) {
			return nil, nil
		}
		promotions, err := environments.ListPromotions(ctx, releaseID)
		if err != nil {
			return nil, err
		}
		converted := make([]dashhttp.ReleasePromotion, 0, len(promotions))
		for _, promotion := range promotions {
			converted = append(converted, dashhttp.ReleasePromotion{
				Environment: promotion.EnvironmentID, Image: promotion.ImageName,
				Tag: promotion.Tag, Digest: promotion.Digest,
				JobID: promotion.JobID, PushedAt: promotion.PushedAt,
			})
		}
		return converted, nil
	}
}
