package worker

import (
	"context"
	"errors"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/provider"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Ce que l'observateur d'environnements doit tenir.
 *
 * Le sujet n'est pas la mécanique — deux appels et une écriture — mais la
 * **hiérarchie des preuves** de B8 : ce que le déployeur déclare est une
 * intention, ce que l'application atteste est un constat, et seul le second
 * autorise à dire « livré ». Les confondre ferait annoncer livré un
 * environnement dont les migrations ont échoué, ce qui est le mensonge le plus
 * coûteux que ce dashboard puisse produire.
 *
 * Écrits au lot D, faute d'avoir trouvé quoi que ce soit ici : la séparation du
 * déployeur et de l'attestation se serait faite sans qu'aucun test ne dise si
 * elle avait cassé le croisement.
 */

type fakeDeployer struct {
	observation provider.Observation
	err         error
	seen        []provider.Target
}

func (f *fakeDeployer) Plan(context.Context, provider.Deployment) (provider.Plan, error) {
	return provider.Plan{}, nil
}

func (f *fakeDeployer) Observe(
	_ context.Context, target provider.Target,
) (provider.Observation, error) {
	f.seen = append(f.seen, target)
	return f.observation, f.err
}

func (f *fakeDeployer) Check(context.Context, []provider.Target) error { return nil }

type fakeAttester struct {
	states []provider.Attestation
	err    error
	calls  int
}

func (f *fakeAttester) Attestations(
	context.Context, string,
) ([]provider.Attestation, error) {
	f.calls++
	return f.states, f.err
}

type fakeEnvironmentStore struct {
	environments []store.Environment
	saved        []store.Observation
}

func (f *fakeEnvironmentStore) UpsertEnvironment(context.Context, store.Environment) error {
	return nil
}

func (f *fakeEnvironmentStore) ListEnvironments(context.Context) ([]store.Environment, error) {
	return f.environments, nil
}

func (f *fakeEnvironmentStore) SaveObservation(_ context.Context, o store.Observation) error {
	f.saved = append(f.saved, o)
	return nil
}

func observerFor(
	t *testing.T, environment store.Environment,
	deployer provider.Deployer, attester provider.Attester,
) (*EnvironmentObserver, *fakeEnvironmentStore) {
	t.Helper()

	environments := &fakeEnvironmentStore{environments: []store.Environment{environment}}
	return NewEnvironmentObserver(environments, deployer, attester, quietLogger()), environments
}

func observable() store.Environment {
	return store.Environment{ID: "dev", Namespace: "mse-dev", Observable: true}
}

// Le déployeur seul ne peut porter qu'une intention. Rendre `portaldata` sur ce
// qu'une Kustomization déclare ferait annoncer livré ce qui n'est qu'appliqué.
func TestSansAttestationLaPreuveResteCelleDuDeployeur(t *testing.T) {
	deployer := &fakeDeployer{observation: provider.Observation{
		Version: "1.8.0", State: "Ready", Resolution: provider.ResolutionDeclared,
	}}
	observer, _ := observerFor(t, observable(), deployer, provider.NoAttester{})

	got := observer.observe(context.Background(), observable())

	if got.Resolution != string(provider.ResolutionDeclared) {
		t.Errorf("résolution %q, attendu %q",
			got.Resolution, provider.ResolutionDeclared)
	}
	if got.DeclaredState != "Ready" {
		t.Errorf("état déclaré %q", got.DeclaredState)
	}
	// Rien n'atteste : le constat applicatif reste vide plutôt que d'emprunter
	// la version que le déployeur déclare.
	if got.PortalVersion != "" {
		t.Errorf("version attestée %q — rien ne l'a attestée", got.PortalVersion)
	}
}

// Le seul cas qui autorise « livré » : l'application rapporte sa version **et**
// que ses migrations ont abouti.
func TestUneAttestationPreteEleveLaPreuve(t *testing.T) {
	deployer := &fakeDeployer{observation: provider.Observation{
		State: "Ready", Resolution: provider.ResolutionDeclared,
	}}
	attester := &fakeAttester{states: []provider.Attestation{
		{Version: "1.8.0", State: "Ready", Ready: true},
	}}
	observer, _ := observerFor(t, observable(), deployer, attester)

	got := observer.observe(context.Background(), observable())

	if got.Resolution != string(provider.ResolutionAttested) {
		t.Errorf("résolution %q, attendu %q", got.Resolution, provider.ResolutionAttested)
	}
	if got.PortalVersion != "1.8.0" {
		t.Errorf("version attestée %q", got.PortalVersion)
	}
}

// Le cas que toute la hiérarchie existe pour attraper : l'image est déployée,
// mais ses migrations ont échoué. Elle tourne sans être exploitable, et la
// preuve doit **redescendre** au niveau de l'intention.
func TestUneAttestationNonPreteNeFaitPasLivre(t *testing.T) {
	deployer := &fakeDeployer{observation: provider.Observation{
		State: "Ready", Resolution: provider.ResolutionDeclared,
	}}
	attester := &fakeAttester{states: []provider.Attestation{
		{Version: "1.8.0", State: "MigrationFailed", Ready: false},
	}}
	observer, _ := observerFor(t, observable(), deployer, attester)

	got := observer.observe(context.Background(), observable())

	if got.Resolution == string(provider.ResolutionAttested) {
		t.Error("des migrations en échec ne peuvent pas valoir attestation")
	}
	// L'état est tout de même conservé : c'est lui qui dit *pourquoi* ce n'est
	// pas livré, et le taire laisserait chercher.
	if got.MigrationState != "MigrationFailed" {
		t.Errorf("état des migrations %q — l'écran doit pouvoir dire pourquoi",
			got.MigrationState)
	}
}

// La production vit dans un cluster que le dashboard ne voit pas. « Non
// observable » n'est pas « inconnu » : sans la distinction, une carte figée
// ressemble à une panne.
func TestUnEnvironnementNonObservableNestPasInterroge(t *testing.T) {
	environment := store.Environment{ID: "prod", Namespace: "mse-prod", Observable: false}
	deployer := &fakeDeployer{observation: provider.Observation{
		Resolution: provider.ResolutionUnobservable,
	}}
	attester := &fakeAttester{}
	observer, _ := observerFor(t, environment, deployer, attester)

	got := observer.observe(context.Background(), environment)

	if got.Resolution != string(provider.ResolutionUnobservable) {
		t.Errorf("résolution %q, attendu %q", got.Resolution, provider.ResolutionUnobservable)
	}
	// L'attestation n'est même pas tentée : interroger un cluster hors de portée
	// produirait une erreur périodique dans les journaux, pour une réponse qu'on
	// connaît déjà.
	if attester.calls != 0 {
		t.Errorf("%d appel(s) à l'attestation d'un environnement hors de portée", attester.calls)
	}
}

// Un déployeur muet ne doit pas emporter l'attestation avec lui : enregistrer ce
// qu'on a vaut mieux que de ne rien enregistrer, et c'est précisément quand le
// déployeur est en panne que le constat applicatif compte le plus.
func TestUnDeployeurEnErreurNempechePasLAttestation(t *testing.T) {
	deployer := &fakeDeployer{err: errors.New("API injoignable")}
	attester := &fakeAttester{states: []provider.Attestation{
		{Version: "1.8.0", State: "Ready", Ready: true},
	}}
	observer, _ := observerFor(t, observable(), deployer, attester)

	got := observer.observe(context.Background(), observable())

	if got.PortalVersion != "1.8.0" {
		t.Errorf("version attestée %q — l'échec du déployeur l'a emportée", got.PortalVersion)
	}
	if got.Resolution != string(provider.ResolutionAttested) {
		t.Errorf("résolution %q — l'attestation reste la meilleure preuve disponible",
			got.Resolution)
	}
}

// Une source d'attestation absente est un cas normal, pas une panne : le
// namespace n'a peut-être jamais été déployé. Le relevé retombe sur ce que le
// déployeur déclare au lieu d'être perdu.
func TestUneAttestationIllisibleRetombeSurLeDeployeur(t *testing.T) {
	deployer := &fakeDeployer{observation: provider.Observation{
		State: "Ready", Resolution: provider.ResolutionDeclared,
	}}
	attester := &fakeAttester{err: errors.New("CRD absent du namespace")}
	observer, _ := observerFor(t, observable(), deployer, attester)

	got := observer.observe(context.Background(), observable())

	if got.Resolution != string(provider.ResolutionDeclared) {
		t.Errorf("résolution %q, attendu %q", got.Resolution, provider.ResolutionDeclared)
	}
	if got.DeclaredState != "Ready" {
		t.Errorf("état déclaré %q — l'échec de l'attestation l'a emporté", got.DeclaredState)
	}
}
