package flux

import "testing"

// La détection des pré-releases décide de tout le garde-fou de B7 : une plage
// sans composante de pré-release ne retient aucune RC, silencieusement.
func TestAcceptsPrerelease(t *testing.T) {
	cases := map[string]bool{
		">=0.0.0-0": true,
		">=0.0.0":   false,
		">=1.8.0-0": true,
		"1.x":       false,
	}

	for semverRange, expected := range cases {
		got := ImagePolicy{Range: semverRange}.AcceptsPrerelease()
		if got != expected {
			t.Errorf("plage %q: pré-releases = %v, attendu %v", semverRange, got, expected)
		}
	}
}

func TestSuffixExtraction(t *testing.T) {
	cases := map[string]string{
		`^(?P<semver>.+)_testing$`:  "testing",
		`^(?P<semver>.+)_unstable$`: "unstable",
		`^(?P<semver>.+)_prod$`:     "prod",

		// Motifs qui ne suivent pas la convention : rendre vide plutôt que
		// deviner. Un suffixe inventé ferait correspondre un environnement à une
		// politique qui ne le concerne pas, et la promotion partirait dans le
		// vide sans que rien ne le signale.
		`^v(\d+)\.(\d+)$`:     "",
		`.*-k8sunstable\..*$`: "",
		`^(?P<semver>.+)_$`:   "",
		`sans-underscore`:     "",
	}

	for pattern, expected := range cases {
		got := ImagePolicy{Pattern: pattern}.Suffix()
		if got != expected {
			t.Errorf("motif %q: suffixe = %q, attendu %q", pattern, got, expected)
		}
	}
}

// `Unknown` veut dire « rapprochement en cours » : le confondre avec un échec
// ferait afficher une erreur pendant chaque déploiement normal.
func TestKustomizationStateDistinguishesReconciling(t *testing.T) {
	cases := map[string]string{
		"True":    "ready",
		"False":   "failed",
		"Unknown": "reconciling",
		"":        "reconciling",
	}

	for ready, expected := range cases {
		if got := (Kustomization{Ready: ready}).State(); got != expected {
			t.Errorf("condition %q: état = %q, attendu %q", ready, got, expected)
		}
	}
}

func TestFindPolicyMatchesOnSuffix(t *testing.T) {
	policies := []ImagePolicy{
		{Name: "portal-unstable", Pattern: `^(?P<semver>.+)_unstable$`, Range: ">=0.0.0-0"},
		{Name: "portal-prod", Pattern: `^(?P<semver>.+)_prod$`, Range: ">=0.0.0"},
	}

	policy, found := FindPolicy(policies, "prod")
	if !found || policy.Name != "portal-prod" {
		t.Fatalf("politique = %+v, trouvée = %v", policy, found)
	}
	if policy.AcceptsPrerelease() {
		t.Error("la politique de production ne doit pas accepter les pré-releases")
	}

	if _, found := FindPolicy(policies, "testing"); found {
		t.Error("un suffixe sans politique ne doit pas être trouvé")
	}
}
