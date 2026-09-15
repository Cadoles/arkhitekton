package mse

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/mse/flux"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/mse/portaldata"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/mse/storage"
)

/*
 * Ce que l'observateur d'environnements doit tenir.
 *
 * Le sujet n'est pas la mécanique — deux appels et une écriture — mais la
 * **hiérarchie des preuves** de B8 : ce que Flux déclare est une intention, ce que
 * l'application atteste est un constat, et seul le second autorise à dire
 * « livré ».
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

func (f *fakeEnvironmentStore) ListEnvironments(context.Context) ([]storage.Environment, error) {
	return f.environments, nil
}

func (f *fakeEnvironmentStore) SaveObservation(_ context.Context, o storage.Observation) error {
	f.saved = append(f.saved, o)
	return nil
}

func observerFor(environment storage.Environment, deployer Deployer, attester Attester) *Observer {
	environments := &fakeEnvironmentStore{environments: []storage.Environment{environment}}
	return NewObserver(environments, deployer, attester, quietLogger())
}

func observable() storage.Environment {
	return storage.Environment{ID: "dev", Namespace: "mse-dev", Observable: true}
}

// Flux seul ne peut porter qu'une intention. Rendre `portaldata` sur ce qu'une
// Kustomization déclare ferait annoncer livré ce qui n'est qu'appliqué.
func TestSansAttestationLaPreuveResteCelleDuDeployeur(t *testing.T) {
	deployer := &fakeDeployer{observation: flux.Observation{
		Version: "1.8.0", State: "Ready", Resolution: flux.ResolutionDeclared,
	}}
	observer := observerFor(observable(), deployer, nil)

	got := observer.observe(context.Background(), observable())

	if got.Resolution != string(flux.ResolutionDeclared) {
		t.Errorf("résolution %q, attendu %q", got.Resolution, flux.ResolutionDeclared)
	}
	if got.DeclaredState != "Ready" {
		t.Errorf("état déclaré %q", got.DeclaredState)
	}
	if got.PortalVersion != "" {
		t.Errorf("version attestée %q — rien ne l'a attestée", got.PortalVersion)
	}
}

// Le seul cas qui autorise « livré » : l'application rapporte sa version **et**
// que ses migrations ont abouti.
func TestUneAttestationPreteEleveLaPreuve(t *testing.T) {
	deployer := &fakeDeployer{observation: flux.Observation{
		State: "Ready", Resolution: flux.ResolutionDeclared,
	}}
	attester := &fakeAttester{states: []portaldata.Attestation{
		{Version: "1.8.0", State: "Ready", Ready: true},
	}}
	observer := observerFor(observable(), deployer, attester)

	got := observer.observe(context.Background(), observable())

	if got.Resolution != string(flux.ResolutionAttested) {
		t.Errorf("résolution %q, attendu %q", got.Resolution, flux.ResolutionAttested)
	}
	if got.PortalVersion != "1.8.0" {
		t.Errorf("version attestée %q", got.PortalVersion)
	}
}

// L'image est déployée, mais ses migrations ont échoué : la preuve doit
// **redescendre** au niveau de l'intention.
func TestUneAttestationNonPreteNeFaitPasLivre(t *testing.T) {
	deployer := &fakeDeployer{observation: flux.Observation{
		State: "Ready", Resolution: flux.ResolutionDeclared,
	}}
	attester := &fakeAttester{states: []portaldata.Attestation{
		{Version: "1.8.0", State: "MigrationFailed", Ready: false},
	}}
	observer := observerFor(observable(), deployer, attester)

	got := observer.observe(context.Background(), observable())

	if got.Resolution == string(flux.ResolutionAttested) {
		t.Error("des migrations en échec ne peuvent pas valoir attestation")
	}
	if got.MigrationState != "MigrationFailed" {
		t.Errorf("état des migrations %q — l'écran doit pouvoir dire pourquoi", got.MigrationState)
	}
}

// La production vit dans un cluster que le dashboard ne voit pas : l'attestation
// n'y est même pas tentée.
func TestUnEnvironnementNonObservableNestPasInterroge(t *testing.T) {
	environment := storage.Environment{ID: "prod", Namespace: "mse-prod", Observable: false}
	deployer := &fakeDeployer{observation: flux.Observation{Resolution: flux.ResolutionUnobservable}}
	attester := &fakeAttester{}
	observer := observerFor(environment, deployer, attester)

	got := observer.observe(context.Background(), environment)

	if got.Resolution != string(flux.ResolutionUnobservable) {
		t.Errorf("résolution %q, attendu %q", got.Resolution, flux.ResolutionUnobservable)
	}
	if attester.calls != 0 {
		t.Errorf("%d appel(s) à l'attestation d'un environnement hors de portée", attester.calls)
	}
}

// Un Flux muet ne doit pas emporter l'attestation avec lui : c'est quand il est
// en panne que le constat applicatif compte le plus.
func TestUnDeployeurEnErreurNempechePasLAttestation(t *testing.T) {
	deployer := &fakeDeployer{err: errors.New("API injoignable")}
	attester := &fakeAttester{states: []portaldata.Attestation{
		{Version: "1.8.0", State: "Ready", Ready: true},
	}}
	observer := observerFor(observable(), deployer, attester)

	got := observer.observe(context.Background(), observable())

	if got.PortalVersion != "1.8.0" {
		t.Errorf("version attestée %q — l'échec du déployeur l'a emportée", got.PortalVersion)
	}
	if got.Resolution != string(flux.ResolutionAttested) {
		t.Errorf("résolution %q — l'attestation reste la meilleure preuve disponible", got.Resolution)
	}
}

// Une source d'attestation illisible est un cas normal : le relevé retombe sur
// ce que Flux déclare au lieu d'être perdu.
func TestUneAttestationIllisibleRetombeSurLeDeployeur(t *testing.T) {
	deployer := &fakeDeployer{observation: flux.Observation{
		State: "Ready", Resolution: flux.ResolutionDeclared,
	}}
	attester := &fakeAttester{err: errors.New("CRD absent du namespace")}
	observer := observerFor(observable(), deployer, attester)

	got := observer.observe(context.Background(), observable())

	if got.Resolution != string(flux.ResolutionDeclared) {
		t.Errorf("résolution %q, attendu %q", got.Resolution, flux.ResolutionDeclared)
	}
	if got.DeclaredState != "Ready" {
		t.Errorf("état déclaré %q — l'échec de l'attestation l'a emporté", got.DeclaredState)
	}
}
