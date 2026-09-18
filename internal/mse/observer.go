package mse

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/mse/flux"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/mse/portaldata"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/mse/storage"
)

// observeInterval espace deux relevés d'environnement.
//
// Un déploiement Flux se compte en minutes : sonder plus souvent n'apprendrait
// rien et multiplierait les appels à l'API du cluster.
const observeInterval = 30 * time.Second

// EnvironmentStore est ce que l'observateur d'environnements attend de la base.
type EnvironmentStore interface {
	UpsertEnvironment(ctx context.Context, environment storage.Environment) error
	ListAllEnvironments(ctx context.Context) ([]storage.Environment, error)
	SaveObservation(ctx context.Context, observation storage.Observation) error
}

// Deployer est ce que l'observateur demande à Flux. Réalisé par [flux.Deployer].
type Deployer interface {
	Observe(ctx context.Context, target flux.Target) (flux.Observation, error)
	Check(ctx context.Context, targets []flux.Target) error
}

// Attester dit quelle version tourne réellement dans un namespace. Réalisé par
// [portaldata.Client].
type Attester interface {
	Attestations(ctx context.Context, namespace string) ([]portaldata.Attestation, error)
}

// Observer tient à jour ce que chaque environnement dit de lui-même.
//
// Deux sources, et leur séparation est le sujet : **Flux** dit ce qu'il a
// appliqué, **PortalData** dit ce qui tourne. C'est leur écart que l'écran des
// environnements existe pour montrer (B8).
type Observer struct {
	store EnvironmentStore
	// deployers porte le déployeur de chaque projet synchronisé : chacun lit le
	// Flux de son namespace. Rempli par [Observer.SyncEnvironments], avant que
	// l'observation ne commence.
	deployers map[int64]Deployer
	// attester est nil quand le cluster ne sert pas le CRD du portail : le relevé
	// retombe alors sur ce que Flux déclare.
	attester Attester
	logger   *slog.Logger
}

func NewObserver(environments EnvironmentStore, attester Attester, logger *slog.Logger) *Observer {
	return &Observer{
		store: environments, deployers: map[int64]Deployer{}, attester: attester, logger: logger,
	}
}

// targetOf traduit un environnement déclaré vers ce que le déployeur attend.
func targetOf(environment storage.Environment) flux.Target {
	return flux.Target{
		ID:         environment.ID,
		Observable: environment.Observable,
		Settings:   environment.DeployerSettings,
	}
}

// ProjectEnvironments porte les environnements qu'un projet déclare au catalogue.
type ProjectEnvironments struct {
	ProjectID    int64
	Slug         string
	Environments []catalog.Environment
	// Deployer lit le Flux du namespace du projet.
	Deployer Deployer
}

// SyncEnvironments écrit en base les environnements que chaque projet déclare,
// puis les soumet à Flux pour qu'il dise s'il saura les atteindre.
//
// Le contrôle se fait **par projet** : deux projets peuvent employer le même
// suffixe de promotion pour des images différentes, et les confronter ensemble
// les ferait passer pour un doublon.
//
// L'ordre compte : la table est d'abord alignée sur la configuration, ensuite
// seulement confrontée au cluster. L'inverse validerait un état que le prochain
// démarrage remplacerait.
func (o *Observer) SyncEnvironments(ctx context.Context, projects []ProjectEnvironments) error {
	var problems []error
	environments := 0
	for _, project := range projects {
		o.deployers[project.ProjectID] = project.Deployer
		targets := make([]flux.Target, 0, len(project.Environments))
		for _, environment := range project.Environments {
			record := storage.Environment{
				ProjectID:        project.ProjectID,
				ID:               environment.ID,
				Label:            environment.Label,
				Rank:             environment.Rank,
				Namespace:        environment.Namespace,
				DeployerSettings: environment.Deployer,
				Observable:       environment.IsObservable(),
				Visible:          environment.IsVisible(),
			}
			if err := o.store.UpsertEnvironment(ctx, record); err != nil {
				return err
			}
			targets = append(targets, targetOf(record))
		}
		environments += len(project.Environments)

		if err := project.Deployer.Check(ctx, targets); err != nil {
			problems = append(problems, fmt.Errorf("projet %s:\n%w", project.Slug, err))
		}
	}

	if err := errors.Join(problems...); err != nil {
		// Refuser de démarrer plutôt que de proposer des promotions sans effet :
		// un tag que Flux ignore ne produit aucune erreur, et l'écart ne se verrait
		// qu'en constatant qu'un environnement ne bouge jamais.
		return fmt.Errorf("environnements en désaccord avec le déployeur:\n%w", err)
	}

	o.logger.Info("environnements validés contre le déployeur",
		"environnements", environments, "projets", len(projects))
	return nil
}

// Run relève périodiquement l'état des environnements.
func (o *Observer) Run(ctx context.Context) error {
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

func (o *Observer) observeAll(ctx context.Context) {
	environments, err := o.store.ListAllEnvironments(ctx)
	if err != nil {
		o.logger.Error("lecture des environnements", "error", err)
		return
	}

	for _, environment := range environments {
		deployer, synced := o.deployers[environment.ProjectID]
		if !synced {
			// Un projet qui a éteint le module garde ses lignes ; il n'est plus
			// observé, et son dernier relevé reste celui d'avant.
			continue
		}
		observation := o.observe(ctx, deployer, environment)
		if err := o.store.SaveObservation(ctx, observation); err != nil {
			o.logger.Error("enregistrement d'une observation", "environnement", environment.ID, "error", err)
		}
	}
}

// observe croise ce que disent Flux et l'application.
//
// `portaldata` atteste qu'une version tourne **et** que ses migrations ont
// abouti ; `git` ne dit que le tag retenu par Flux ; `unobservable` dit qu'on ne
// peut rien affirmer (B8).
func (o *Observer) observe(ctx context.Context, deployer Deployer, environment storage.Environment) storage.Observation {
	observation := storage.Observation{
		ProjectID:     environment.ProjectID,
		EnvironmentID: environment.ID,
		Resolution:    string(flux.ResolutionUnobservable),
	}

	// Ce que Flux a appliqué : une **intention**. Un échec de lecture n'interrompt
	// pas — l'attestation reste peut-être lisible.
	declared, err := deployer.Observe(ctx, targetOf(environment))
	if err != nil {
		o.logger.Error("lecture de l'état du déployeur",
			"environnement", environment.ID, "error", err)
	} else {
		observation.DeclaredState = declared.State
		observation.Resolution = string(declared.Resolution)
	}

	if !environment.Observable || o.attester == nil {
		// La production, dans son cluster indépendant, ou un cluster sans le CRD
		// du portail. « Non observable » et non « inconnu » : sans la distinction,
		// une carte figée ressemble à une panne.
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
			observation.Resolution = string(flux.ResolutionAttested)
		}
		break
	}

	return observation
}
