package flux

import (
	"forge.cadoles.com/vcarroy/arkhitekton/internal/version"
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

// La règle reprise du catalogue au lot D : deux environnements ne peuvent pas
// partager un suffixe.
//
// Elle vivait dans la validation du catalogue, où elle imposait un concept de
// Flux à tout catalogue — y compris à celui d'un projet sans déployeur, qui ne
// pouvait alors déclarer aucun environnement. Elle appartient au déployeur qui
// sélectionne par suffixe, et à lui seul.
func TestDeuxEnvironnementsNePeuventPartagerUnSuffixe(t *testing.T) {
	environments := []Environment{
		{ID: "qualif", PromotionSuffix: "testing", AcceptsPrerelease: true, Observable: true},
		{ID: "preprod", PromotionSuffix: "testing", AcceptsPrerelease: true, Observable: true},
	}

	err := CheckPromotionTargets(environments, nil)
	if err == nil {
		t.Fatal("deux environnements au même suffixe: l'un ne serait jamais déployé")
	}
	if !strings.Contains(err.Error(), "partagent le suffixe") {
		t.Errorf("message: %v — il doit nommer le conflit", err)
	}
}

// Un environnement sans suffixe ne peut pas être atteint par **ce** déployeur :
// il sélectionne par métadonnée SemVer et ne saurait pas quoi pousser. Le
// refuser ici plutôt que dans le catalogue est ce qui laisse un autre déployeur
// accepter le même environnement.
func TestUnEnvironnementSansSuffixeEstRefuseParCeDeployeur(t *testing.T) {
	err := CheckPromotionTargets([]Environment{{ID: "dev", Observable: true}}, nil)
	if err == nil {
		t.Fatal("un environnement sans suffixe doit être refusé par un déployeur qui en a besoin")
	}
}

// Le conflit se voit même sur des environnements non observables : deux
// environnements homonymes se marchent dessus indépendamment de notre capacité à
// lire leurs politiques.
func TestLeConflitDeSuffixeSeVoitHorsDePortee(t *testing.T) {
	environments := []Environment{
		{ID: "prod", PromotionSuffix: "prod", Observable: false},
		{ID: "prod-bis", PromotionSuffix: "prod", Observable: false},
	}

	if err := CheckPromotionTargets(environments, nil); err == nil {
		t.Error("le conflit ne dépend pas de l'observabilité")
	}
}

// Le couplage schéma × déployeur, refusé plutôt que découvert.
//
// Ce déployeur promeut en écrivant une métadonnée dans le tag, que la plage
// d'une ImagePolicy retient. Un schéma qui ne sait pas où la poser rend la
// promotion impossible — et l'échec serait le plus coûteux qui soit : le tag
// partirait, le registre l'accepterait, Flux l'ignorerait, et la promotion
// paraîtrait réussie (B7, doc 12 §12.4).
func TestUnSchemaOpaqueEstRefuseParCeDeployeur(t *testing.T) {
	err := NewDeployer(nil, DefaultPromotionKind, "").CheckScheme(version.Opaque{})
	if err == nil {
		t.Fatal("un schéma sans marqueur doit être refusé: la promotion serait sans effet")
	}
	// Le message doit dire *pourquoi*, sans quoi on cherchera du côté du
	// registre ou des politiques.
	if !strings.Contains(err.Error(), "marqueur") {
		t.Errorf("message %q — il doit nommer ce qui manque", err)
	}
}

// Le SemVer convient : c'est la grammaire que les ImagePolicy sélectionnent.
func TestUnSchemaQuiSaitMarquerEstAccepte(t *testing.T) {
	if err := NewDeployer(nil, DefaultPromotionKind, "").CheckScheme(version.SemVer{}); err != nil {
		t.Errorf("schéma SemVer refusé: %v", err)
	}
}

// Le marqueur se pose en underscore : la grammaire des tags OCI exclut le `+`
// que le SemVer réserve à la métadonnée de build (B7).
func TestLeMarqueurSePoseEnUnderscore(t *testing.T) {
	got, err := Mark("1.8.0-rc1", "testing")
	if err != nil {
		t.Fatalf("marquage: %v", err)
	}
	if got != "1.8.0-rc1_testing" {
		t.Errorf("tag marqué = %q", got)
	}
	// Sans marqueur, le tag ressort inchangé plutôt que suffixé d'un underscore
	// orphelin, que le registre accepterait sans que rien ne le sélectionne.
	if got, _ := Mark("1.8.0", ""); got != "1.8.0" {
		t.Errorf("tag sans marqueur = %q", got)
	}
}
