package mse

import (
	"context"

	dashhttp "forge.cadoles.com/vcarroy/mse-dashboard/internal/http"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/mse/storage"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plugin"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

// Backend réunit les deux magasins dont les routes du plugin ont besoin : celui
// du noyau pour les releases, les projets, la file et l'audit, celui du plugin
// pour ses propres tables.
type Backend struct {
	*store.DB
	*storage.Store
}

func NewBackend(db *store.DB) Backend {
	return Backend{DB: db, Store: storage.New(db.Pool())}
}

// Deployments rend, par tag de release, les environnements qui la font tourner.
//
// Le noyau affiche la pastille « déployée sur » sans lire les tables du plugin :
// il appelle cette fonction, qui ne rend rien quand le module est éteint pour le
// projet (doc 16 §16.17, §16.18).
func Deployments(environments *storage.Store, modules *dashhttp.Modules) func(context.Context, int64) (map[string][]string, error) {
	return func(ctx context.Context, projectID int64) (map[string][]string, error) {
		if !modules.EnabledIn(ctx, projectID, plugin.ModuleMSE) {
			return nil, nil
		}
		return environments.DeployedByRelease(ctx)
	}
}

// Promotions rend les promotions demandées d'une release, dans le vocabulaire du
// noyau. Rien quand le module est éteint.
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
