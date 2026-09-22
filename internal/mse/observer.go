package mse

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/flux"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/portaldata"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/storage"
)

// observeInterval spaces two environment readings.
//
// A Flux deployment is counted in minutes: probing more often would teach nothing and multiply
// the calls to the cluster's API.
const observeInterval = 30 * time.Second

// EnvironmentStore is what the environment observer needs from the database.
type EnvironmentStore interface {
	UpsertEnvironment(ctx context.Context, environment storage.Environment) error
	ListAllEnvironments(ctx context.Context) ([]storage.Environment, error)
	SaveObservation(ctx context.Context, observation storage.Observation) error
}

// Deployer is what the observer asks of Flux. Implemented by [flux.Deployer].
type Deployer interface {
	Observe(ctx context.Context, target flux.Target) (flux.Observation, error)
	Check(ctx context.Context, targets []flux.Target) error
}

// Attester says which version is really running in a namespace. Implemented by
// [portaldata.Client].
type Attester interface {
	Attestations(ctx context.Context, namespace string) ([]portaldata.Attestation, error)
}

// Observer keeps up to date what each environment says about itself.
//
// Two sources, and their separation is the subject: **Flux** says what it applied,
// **PortalData** says what is running. It is their gap that the environments screen exists to
// show.
type Observer struct {
	store EnvironmentStore
	// deployers carries each synchronised project's deployer: each reads the Flux of its
	// namespace. Filled by [Observer.SyncEnvironments], before the observation begins.
	deployers map[int64]Deployer
	// attester is nil when the cluster does not serve the portal's CRD: the reading then falls back
	// to what Flux declares.
	attester Attester
	logger   *slog.Logger
}

func NewObserver(environments EnvironmentStore, attester Attester, logger *slog.Logger) *Observer {
	return &Observer{
		store: environments, deployers: map[int64]Deployer{}, attester: attester, logger: logger,
	}
}

// targetOf translates a declared environment into what the deployer needs.
func targetOf(environment storage.Environment) flux.Target {
	return flux.Target{
		ID:         environment.ID,
		Observable: environment.Observable,
		Settings:   environment.DeployerSettings,
	}
}

// ProjectEnvironments carries the environments a project declares in the catalogue.
type ProjectEnvironments struct {
	ProjectID    int64
	Slug         string
	Environments []catalog.Environment
	// Deployer reads the Flux of the project's namespace.
	Deployer Deployer
}

// SyncEnvironments writes into the database the environments each project declares, then
// submits them to Flux so that it says whether it will be able to reach them.
//
// The check is done **per project**: two projects may use the same promotion suffix for
// different images, and confronting them together would make them look like a duplicate.
//
// The order counts: the table is first aligned with the configuration, and only then confronted
// with the cluster. The reverse would validate a state the next start-up would replace.
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
		// Refuse to start rather than offer promotions with no effect: a tag Flux ignores produces no
		// error, and the gap would show only by noticing that an environment never moves.
		return fmt.Errorf("environments at odds with the deployer:\n%w", err)
	}

	o.logger.Info("environments validated against the deployer",
		"environments", environments, "projects", len(projects))
	return nil
}

// Run periodically reads the state of the environments.
func (o *Observer) Run(ctx context.Context) error {
	ticker := time.NewTicker(observeInterval)
	defer ticker.Stop()

	for {
		o.observeAll(ctx)

		select {
		case <-ctx.Done():
			o.logger.Info("environment observation stopped")
			return nil
		case <-ticker.C:
		}
	}
}

func (o *Observer) observeAll(ctx context.Context) {
	environments, err := o.store.ListAllEnvironments(ctx)
	if err != nil {
		o.logger.Error("reading the environments", "error", err)
		return
	}

	for _, environment := range environments {
		deployer, synced := o.deployers[environment.ProjectID]
		if !synced {
			// A project that switched the module off keeps its rows; it is no longer observed, and its last
			// reading stays the one from before.
			continue
		}
		observation := o.observe(ctx, deployer, environment)
		if err := o.store.SaveObservation(ctx, observation); err != nil {
			o.logger.Error("recording an observation", "environment", environment.ID, "error", err)
		}
	}
}

// observe crosses what Flux and the application say.
//
// `portaldata` attests that a version is running **and** that its migrations completed; `git`
// only says the tag Flux kept; `unobservable` says nothing can be asserted.
func (o *Observer) observe(ctx context.Context, deployer Deployer, environment storage.Environment) storage.Observation {
	observation := storage.Observation{
		ProjectID:     environment.ProjectID,
		EnvironmentID: environment.ID,
		Resolution:    string(flux.ResolutionUnobservable),
	}

	// What Flux applied: an **intention**. A read failure does not interrupt — the attestation may
	// still be readable.
	declared, err := deployer.Observe(ctx, targetOf(environment))
	if err != nil {
		o.logger.Error("reading the deployer's state",
			"environment", environment.ID, "error", err)
	} else {
		observation.DeclaredState = declared.State
		observation.Resolution = string(declared.Resolution)
	}

	if !environment.Observable || o.attester == nil {
		// Production, in its independent cluster, or a cluster without the portal's CRD. "Not
		// observable" and not "unknown": without the distinction, a frozen card looks like an outage.
		return observation
	}

	states, err := o.attester.Attestations(ctx, environment.Namespace)
	if err != nil {
		// The source may be absent from an application namespace that has not been deployed yet: it is
		// not an anomaly of the dashboard.
		o.logger.Debug("attestation unreadable", "environment", environment.ID, "error", err)
		return observation
	}

	for _, state := range states {
		observation.PortalVersion = state.Version
		observation.MigrationState = state.State
		if state.Ready {
			// The only case where a version can be asserted delivered: it is running, and its migrations
			// completed.
			observation.Resolution = string(flux.ResolutionAttested)
		}
		break
	}

	return observation
}
