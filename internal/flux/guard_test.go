package flux

import (
	"strings"
	"testing"
)

func realPolicies() []ImagePolicy {
	// Les quatre politiques telles qu'elles sont déclarées sur le cluster de
	// développement, et telles que B7 les décrit.
	return []ImagePolicy{
		{Name: "portal-unstable", Pattern: `^(?P<semver>.+)_unstable$`, Range: ">=0.0.0-0"},
		{Name: "portal-testing", Pattern: `^(?P<semver>.+)_testing$`, Range: ">=0.0.0-0"},
		{Name: "portal-preprod", Pattern: `^(?P<semver>.+)_preprod$`, Range: ">=0.0.0-0"},
		{Name: "portal-prod", Pattern: `^(?P<semver>.+)_prod$`, Range: ">=0.0.0"},
	}
}

func realEnvironments() []Environment {
	return []Environment{
		{ID: "unstable", PromotionSuffix: "unstable", AcceptsPrerelease: true, Observable: true},
		// Le piège de B7 : qualif est désigné `testing` par Flux.
		{ID: "qualif", PromotionSuffix: "testing", AcceptsPrerelease: true, Observable: true},
		{ID: "preprod", PromotionSuffix: "preprod", AcceptsPrerelease: true, Observable: true},
		{ID: "prod", PromotionSuffix: "prod", AcceptsPrerelease: false, Observable: true},
	}
}

func TestGuardAcceptsTheDocumentedConfiguration(t *testing.T) {
	if err := CheckPromotionTargets(realEnvironments(), realPolicies()); err != nil {
		t.Errorf("la configuration de B7 est refusée: %v", err)
	}
}

// Le mode de défaillance que tout le garde-fou existe pour empêcher : un suffixe
// que nulle politique ne retient produit un tag valide, poussé sans erreur, que
// Flux ignore. La promotion paraît réussie et rien ne se déploie.
func TestGuardDetectsOrphanSuffix(t *testing.T) {
	environments := realEnvironments()
	// L'erreur la plus probable en pratique : écrire `qualif` là où Flux attend
	// `testing`.
	environments[1].PromotionSuffix = "qualif"

	err := CheckPromotionTargets(environments, realPolicies())
	if err == nil {
		t.Fatal("un suffixe orphelin doit être détecté")
	}
	if !strings.Contains(err.Error(), "qualif") {
		t.Errorf("le message ne nomme pas le suffixe fautif: %v", err)
	}
	// Le message liste ce qui existe : sans cela, corriger suppose d'aller
	// interroger le cluster à la main.
	if !strings.Contains(err.Error(), "testing") {
		t.Errorf("le message ne liste pas les suffixes disponibles: %v", err)
	}
}

// Une table qui déclare accepter les RC là où la plage les exclut fait proposer
// une promotion que Flux ignorera. L'inverse fait refuser une promotion qui
// aurait fonctionné.
func TestGuardDetectsPrereleaseDisagreement(t *testing.T) {
	environments := realEnvironments()
	environments[3].AcceptsPrerelease = true // prod, dont la plage est >=0.0.0

	err := CheckPromotionTargets(environments, realPolicies())
	if err == nil {
		t.Fatal("un désaccord sur les pré-releases doit être détecté")
	}
	for _, expected := range []string{"prod", "portal-prod", ">=0.0.0"} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("le message ne contient pas %q: %v", expected, err)
		}
	}
}

// La production vit dans un cluster indépendant : ses ImagePolicy ne sont pas
// lisibles d'ici, et les exiger ferait échouer le démarrage sur une absence
// normale.
func TestGuardSkipsUnobservableEnvironments(t *testing.T) {
	environments := realEnvironments()
	environments[3].Observable = false
	environments[3].PromotionSuffix = "suffixe-invisible"

	policies := realPolicies()[:3] // pas de politique de production ici

	if err := CheckPromotionTargets(environments, policies); err != nil {
		t.Errorf("un environnement non observable a été vérifié: %v", err)
	}
}

// Le cas de la transition SemVer : les politiques de production emploient encore
// le canal mktools, dont aucun suffixe n'est extractible. Le garde-fou doit le
// dire clairement plutôt que de laisser croire à une correspondance.
func TestGuardReportsUnrecognisedPatterns(t *testing.T) {
	legacy := []ImagePolicy{
		{Name: "portal-unstable", Pattern: `^.*-k8sunstable\..*$`, Range: ">=0.0.0-0"},
	}
	environments := []Environment{
		{ID: "unstable", PromotionSuffix: "unstable", AcceptsPrerelease: true, Observable: true},
	}

	err := CheckPromotionTargets(environments, legacy)
	if err == nil {
		t.Fatal("un motif non reconnu doit produire un écart")
	}
	if !strings.Contains(err.Error(), "suffixe non reconnu") {
		t.Errorf("le message n'explique pas que le motif n'est pas exploitable: %v", err)
	}
}
