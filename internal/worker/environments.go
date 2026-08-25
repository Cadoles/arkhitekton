package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/flux"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

// observeInterval espace deux relevés d'environnement.
//
// Un déploiement Flux se compte en minutes : sonder plus souvent n'apprendrait
// rien et multiplierait les appels à l'API du cluster.
const observeInterval = 30 * time.Second

// EnvironmentStore est ce que l'observateur d'environnements attend de la base.
type EnvironmentStore interface {
	UpsertEnvironment(ctx context.Context, environment store.Environment) error
	ListEnvironments(ctx context.Context) ([]store.Environment, error)
	SaveObservation(ctx context.Context, observation store.Observation) error
}

// EnvironmentObserver tient à jour ce que chaque environnement dit de lui-même.
type EnvironmentObserver struct {
	store  EnvironmentStore
	flux   *flux.Client
	logger *slog.Logger
}

func NewEnvironmentObserver(environments EnvironmentStore, client *flux.Client, logger *slog.Logger) *EnvironmentObserver {
	return &EnvironmentObserver{store: environments, flux: client, logger: logger}
}

// SyncEnvironments écrit en base les environnements déclarés, puis vérifie que
// leurs suffixes de promotion correspondent aux ImagePolicy réelles.
//
// L'ordre compte : la table est d'abord alignée sur la configuration, ensuite
// seulement confrontée au cluster. L'inverse validerait un état que le prochain
// démarrage remplacerait.
func (o *EnvironmentObserver) SyncEnvironments(ctx context.Context, declared []catalog.Environment) error {
	for _, environment := range declared {
		record := store.Environment{
			ID:                environment.ID,
			Label:             environment.Label,
			PromotionSuffix:   environment.PromotionSuffix,
			AcceptsPrerelease: environment.AcceptsPrerelease,
			Rank:              environment.Rank,
			Namespace:         environment.Namespace,
			FluxKustomization: environment.FluxKustomization,
			Observable:        environment.IsObservable(),
			Visible:           environment.IsVisible(),
		}
		if err := o.store.UpsertEnvironment(ctx, record); err != nil {
			return err
		}
	}

	policies, err := o.flux.ImagePolicies(ctx)
	if err != nil {
		return fmt.Errorf("lecture des ImagePolicy: %w", err)
	}

	targets := make([]flux.Environment, 0, len(declared))
	for _, environment := range declared {
		targets = append(targets, flux.Environment{
			ID:                environment.ID,
			PromotionSuffix:   environment.PromotionSuffix,
			AcceptsPrerelease: environment.AcceptsPrerelease,
			Observable:        environment.IsObservable(),
		})
	}

	if err := flux.CheckPromotionTargets(targets, policies); err != nil {
		// Refuser de démarrer plutôt que de proposer des promotions sans effet :
		// un tag poussé que Flux ignore ne produit aucune erreur, et l'écart ne
		// se verrait qu'en constatant qu'un environnement ne bouge jamais.
		return fmt.Errorf("table des environnements en désaccord avec les ImagePolicy:\n%w", err)
	}

	o.logger.Info("environnements validés contre les ImagePolicy",
		"environnements", len(declared), "politiques", len(policies))
	return nil
}

// Run relève périodiquement l'état des environnements.
func (o *EnvironmentObserver) Run(ctx context.Context) error {
	ticker := time.NewTicker(observeInterval)
	defer ticker.Stop()

	for {
		o.observeAll(ctx)

		select {
		case <-ctx.Done():
			o.logger.Info("arrêt de l'observation des environnements")
			return nil
		case <-ticker.C:
		}
	}
}

func (o *EnvironmentObserver) observeAll(ctx context.Context) {
	environments, err := o.store.ListEnvironments(ctx)
	if err != nil {
		o.logger.Error("lecture des environnements", "error", err)
		return
	}

	kustomizations, err := o.flux.Kustomizations(ctx)
	if err != nil {
		o.logger.Error("lecture des Kustomization", "error", err)
		// L'état Flux manque, mais PortalData reste peut-être lisible : on
		// continue avec ce qu'on a plutôt que de ne rien enregistrer.
		kustomizations = nil
	}

	for _, environment := range environments {
		observation := o.observe(ctx, environment, kustomizations)
		if err := o.store.SaveObservation(ctx, observation); err != nil {
			o.logger.Error("enregistrement d'une observation", "environnement", environment.ID, "error", err)
		}
	}
}

// observe croise ce que disent Flux et l'application.
//
// La `resolution` dit d'où vient l'information, et la distinction est
// fonctionnelle : `portaldata` atteste qu'une version tourne **et** que ses
// migrations ont abouti ; `git` ne dit que le tag retenu par Flux ; `unobservable`
// dit qu'on ne peut rien affirmer. Les confondre ferait annoncer « livré » un
// environnement dont les migrations ont échoué (B8).
func (o *EnvironmentObserver) observe(
	ctx context.Context, environment store.Environment, kustomizations []flux.Kustomization,
) store.Observation {
	observation := store.Observation{EnvironmentID: environment.ID, Resolution: "unobservable"}

	if !environment.Observable {
		// La production, dans son cluster indépendant. L'état est « non
		// observable » et non « inconnu » : l'interface doit distinguer les deux,
		// sans quoi une carte figée ressemble à une panne.
		return observation
	}

	for _, kustomization := range kustomizations {
		if kustomization.Name == environment.FluxKustomization {
			observation.FluxState = kustomization.State()
			observation.Resolution = "git"
			break
		}
	}

	states, err := o.flux.Attestations(ctx, environment.Namespace)
	if err != nil {
		// La source peut être absente d'un namespace applicatif qui n'a pas
		// encore été déployé : ce n'est pas une anomalie du dashboard.
		o.logger.Debug("attestation illisible", "environnement", environment.ID, "error", err)
		return observation
	}

	for _, state := range states {
		observation.PortalVersion = state.Version
		observation.MigrationState = state.State
		if state.Ready {
			// Seul cas où l'on peut affirmer qu'une version est livrée : elle
			// tourne, et ses migrations ont abouti.
			observation.Resolution = "portaldata"
		}
		break
	}

	return observation
}
