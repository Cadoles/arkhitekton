package worker

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/flux"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/provider"
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
//
// Deux sources, et leur séparation est le sujet : le **déployeur** dit ce qu'il a
// appliqué, l'**attestation** dit ce qui tourne. C'est leur écart que l'écran des
// environnements existe pour montrer, et les fondre en une seule dépendance le
// ferait disparaître (B8, doc 12 §12.5).
type EnvironmentObserver struct {
	store    EnvironmentStore
	deployer provider.Deployer
	attester provider.Attester
	logger   *slog.Logger
}

func NewEnvironmentObserver(
	environments EnvironmentStore, deployer provider.Deployer,
	attester provider.Attester, logger *slog.Logger,
) *EnvironmentObserver {
	return &EnvironmentObserver{
		store: environments, deployer: deployer, attester: attester, logger: logger,
	}
}

// targetOf traduit un environnement déclaré vers ce que la frontière attend.
//
// Les réglages propres au déployeur sont passés dans **son** vocabulaire : le
// suffixe de promotion et l'acceptation des pré-releases n'ont de sens que pour
// une sélection par métadonnée SemVer, et les inscrire dans le type commun les
// imposerait à un déployeur qui procède autrement.
func targetOf(environment store.Environment) provider.Target {
	return provider.Target{
		ID:         environment.ID,
		Observable: environment.Observable,
		Settings: map[string]string{
			flux.SettingSuffix:            environment.PromotionSuffix,
			flux.SettingAcceptsPrerelease: strconv.FormatBool(environment.AcceptsPrerelease),
			flux.SettingKustomization:     environment.FluxKustomization,
		},
	}
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

	targets := make([]provider.Target, 0, len(declared))
	for _, environment := range declared {
		targets = append(targets, targetOf(store.Environment{
			ID:                environment.ID,
			PromotionSuffix:   environment.PromotionSuffix,
			AcceptsPrerelease: environment.AcceptsPrerelease,
			FluxKustomization: environment.FluxKustomization,
			Observable:        environment.IsObservable(),
		}))
	}

	if err := o.deployer.Check(ctx, targets); err != nil {
		// Refuser de démarrer plutôt que de proposer des promotions sans effet :
		// un acheminement que l'outil ignore ne produit aucune erreur, et l'écart
		// ne se verrait qu'en constatant qu'un environnement ne bouge jamais.
		return fmt.Errorf("environnements en désaccord avec le déployeur:\n%w", err)
	}

	o.logger.Info("environnements validés contre le déployeur", "environnements", len(declared))
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

	for _, environment := range environments {
		observation := o.observe(ctx, environment)
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
	ctx context.Context, environment store.Environment,
) store.Observation {
	observation := store.Observation{
		EnvironmentID: environment.ID,
		Resolution:    string(provider.ResolutionUnobservable),
	}

	// Ce que le déployeur a appliqué : une **intention**. Un échec de lecture
	// n'interrompt pas — l'attestation reste peut-être lisible, et enregistrer ce
	// qu'on a vaut mieux que de ne rien enregistrer.
	declared, err := o.deployer.Observe(ctx, targetOf(environment))
	if err != nil {
		o.logger.Error("lecture de l'état du déployeur",
			"environnement", environment.ID, "error", err)
	} else {
		observation.FluxState = declared.State
		observation.Resolution = string(declared.Resolution)
	}

	if !environment.Observable {
		// La production, dans son cluster indépendant. L'état est « non
		// observable » et non « inconnu » : l'interface doit distinguer les deux,
		// sans quoi une carte figée ressemble à une panne.
		return observation
	}

	states, err := o.attester.Attestations(ctx, environment.Namespace)
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
			observation.Resolution = string(provider.ResolutionAttested)
		}
		break
	}

	return observation
}
