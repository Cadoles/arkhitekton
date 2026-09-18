package mse

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/flux"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/portaldata"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/storage"
)

/*
 * What the environment observer must hold.
 *
 * The subject is not the mechanics — two calls and a write — but the **hierarchy of proofs**:
 * what Flux declares is an intention, what the application attests is an observation, and only
 * the second allows saying "delivered".
 */

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fakeDeployer struct {
	observation flux.Observation
	err         error
	seen        []flux.Target
}

func (f *fakeDeployer) Observe(_ context.Context, target flux.Target) (flux.Observation, error) {
	f.seen = append(f.seen, target)
	return f.observation, f.err
}

func (f *fakeDeployer) Check(context.Context, []flux.Target) error { return nil }

type fakeAttester struct {
	states []portaldata.Attestation
	err    error
	calls  int
}

func (f *fakeAttester) Attestations(context.Context, string) ([]portaldata.Attestation, error) {
	f.calls++
	return f.states, f.err
}

type fakeEnvironmentStore struct {
	environments []storage.Environment
	saved        []storage.Observation
}

func (f *fakeEnvironmentStore) UpsertEnvironment(context.Context, storage.Environment) error {
	return nil
}

func (f *fakeEnvironmentStore) ListAllEnvironments(context.Context) ([]storage.Environment, error) {
	return f.environments, nil
}

func (f *fakeEnvironmentStore) SaveObservation(_ context.Context, o storage.Observation) error {
	f.saved = append(f.saved, o)
	return nil
}

func observerFor(environment storage.Environment, attester Attester) *Observer {
	environments := &fakeEnvironmentStore{environments: []storage.Environment{environment}}
	return NewObserver(environments, attester, quietLogger())
}

func observable() storage.Environment {
	return storage.Environment{ID: "dev", Namespace: "mse-dev", Observable: true}
}

// Flux alone can carry only an intention. Returning `portaldata` for what a Kustomization
// declares would announce as delivered what is merely applied.
func TestWithNoAttestationTheProofStaysTheDeployers(t *testing.T) {
	deployer := &fakeDeployer{observation: flux.Observation{
		Version: "1.8.0", State: "Ready", Resolution: flux.ResolutionDeclared,
	}}
	observer := observerFor(observable(), nil)

	got := observer.observe(context.Background(), deployer, observable())

	if got.Resolution != string(flux.ResolutionDeclared) {
		t.Errorf("resolution %q, expected %q", got.Resolution, flux.ResolutionDeclared)
	}
	if got.DeclaredState != "Ready" {
		t.Errorf("declared state %q", got.DeclaredState)
	}
	if got.PortalVersion != "" {
		t.Errorf("version attested %q — nothing attested it", got.PortalVersion)
	}
}

// The only case that allows "delivered": the application reports its version **and** that its
// migrations completed.
func TestAReadyAttestationRaisesTheProof(t *testing.T) {
	deployer := &fakeDeployer{observation: flux.Observation{
		State: "Ready", Resolution: flux.ResolutionDeclared,
	}}
	attester := &fakeAttester{states: []portaldata.Attestation{
		{Version: "1.8.0", State: "Ready", Ready: true},
	}}
	observer := observerFor(observable(), attester)

	got := observer.observe(context.Background(), deployer, observable())

	if got.Resolution != string(flux.ResolutionAttested) {
		t.Errorf("resolution %q, expected %q", got.Resolution, flux.ResolutionAttested)
	}
	if got.PortalVersion != "1.8.0" {
		t.Errorf("version attested %q", got.PortalVersion)
	}
}

// The image is deployed, but its migrations failed: the proof must **step back down** to the
// level of the intention.
func TestAnUnreadyAttestationDoesNotMakeItDelivered(t *testing.T) {
	deployer := &fakeDeployer{observation: flux.Observation{
		State: "Ready", Resolution: flux.ResolutionDeclared,
	}}
	attester := &fakeAttester{states: []portaldata.Attestation{
		{Version: "1.8.0", State: "MigrationFailed", Ready: false},
	}}
	observer := observerFor(observable(), attester)

	got := observer.observe(context.Background(), deployer, observable())

	if got.Resolution == string(flux.ResolutionAttested) {
		t.Error("failed migrations cannot count as an attestation")
	}
	if got.MigrationState != "MigrationFailed" {
		t.Errorf("migration state %q — the screen must be able to say why", got.MigrationState)
	}
}

// Production lives in a cluster the dashboard does not see: the attestation is not even
// attempted there.
func TestAnUnobservableEnvironmentIsNotQueried(t *testing.T) {
	environment := storage.Environment{ID: "prod", Namespace: "mse-prod", Observable: false}
	deployer := &fakeDeployer{observation: flux.Observation{Resolution: flux.ResolutionUnobservable}}
	attester := &fakeAttester{}
	observer := observerFor(environment, attester)

	got := observer.observe(context.Background(), deployer, environment)

	if got.Resolution != string(flux.ResolutionUnobservable) {
		t.Errorf("resolution %q, expected %q", got.Resolution, flux.ResolutionUnobservable)
	}
	if attester.calls != 0 {
		t.Errorf("%d call(s) to the attestation of an environment out of reach", attester.calls)
	}
}

// A mute Flux must not carry off the attestation with it: it is when Flux is down that the
// application's own observation counts most.
func TestAFailingDeployerDoesNotPreventTheAttestation(t *testing.T) {
	deployer := &fakeDeployer{err: errors.New("API injoignable")}
	attester := &fakeAttester{states: []portaldata.Attestation{
		{Version: "1.8.0", State: "Ready", Ready: true},
	}}
	observer := observerFor(observable(), attester)

	got := observer.observe(context.Background(), deployer, observable())

	if got.PortalVersion != "1.8.0" {
		t.Errorf("version attested %q — the deployer's failure won", got.PortalVersion)
	}
	if got.Resolution != string(flux.ResolutionAttested) {
		t.Errorf("resolution %q — the attestation stays the best proof available", got.Resolution)
	}
}

// An unreadable attestation source is a normal case: the reading falls back to what Flux
// declares instead of being lost.
func TestAnUnreadableAttestationFallsBackToTheDeployer(t *testing.T) {
	deployer := &fakeDeployer{observation: flux.Observation{
		State: "Ready", Resolution: flux.ResolutionDeclared,
	}}
	attester := &fakeAttester{err: errors.New("CRD missing from the namespace")}
	observer := observerFor(observable(), attester)

	got := observer.observe(context.Background(), deployer, observable())

	if got.Resolution != string(flux.ResolutionDeclared) {
		t.Errorf("resolution %q, expected %q", got.Resolution, flux.ResolutionDeclared)
	}
	if got.DeclaredState != "Ready" {
		t.Errorf("declared state %q — the attestation's failure won", got.DeclaredState)
	}
}

// Each project is observed by its namespace's deployer, and a project that was not synchronised
// — module switched off — is no longer observed at all.
func TestEachProjectIsObservedByItsDeployer(t *testing.T) {
	mse := &fakeDeployer{observation: flux.Observation{Resolution: flux.ResolutionDeclared}}
	themes := &fakeDeployer{observation: flux.Observation{Resolution: flux.ResolutionDeclared}}
	environments := &fakeEnvironmentStore{environments: []storage.Environment{
		{ProjectID: 1, ID: "qualif", Namespace: "mse-dev", Observable: true},
		{ProjectID: 2, ID: "qualif", Namespace: "themes-dev", Observable: true},
		{ProjectID: 3, ID: "qualif", Namespace: "eteint", Observable: true},
	}}
	observer := NewObserver(environments, nil, quietLogger())
	ctx := context.Background()

	if err := observer.SyncEnvironments(ctx, []ProjectEnvironments{
		{ProjectID: 1, Slug: "mse", Deployer: mse,
			Environments: []catalog.Environment{{ID: "qualif", Namespace: "mse-dev"}}},
		{ProjectID: 2, Slug: "themes", Deployer: themes,
			Environments: []catalog.Environment{{ID: "qualif", Namespace: "themes-dev"}}},
	}); err != nil {
		t.Fatalf("synchronisation: %v", err)
	}
	observer.observeAll(ctx)

	if len(mse.seen) != 1 || len(themes.seen) != 1 {
		t.Errorf("readings per deployer: mse %d, themes %d — expected one each", len(mse.seen), len(themes.seen))
	}
	observed := map[int64]bool{}
	for _, observation := range environments.saved {
		observed[observation.ProjectID] = true
	}
	if !observed[1] || !observed[2] || observed[3] {
		t.Errorf("projects read = %v, expected 1 and 2 only", observed)
	}
}
