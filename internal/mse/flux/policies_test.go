package flux

import "testing"

// The detection of pre-releases decides the whole guard rail: a range with no pre-release
// component keeps no RC, silently.
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
			t.Errorf("range %q: pre-releases = %v, expected %v", semverRange, got, expected)
		}
	}
}

func TestSuffixExtraction(t *testing.T) {
	cases := map[string]string{
		`^(?P<semver>.+)_testing$`:  "testing",
		`^(?P<semver>.+)_unstable$`: "unstable",
		`^(?P<semver>.+)_prod$`:     "prod",

		// Patterns that do not follow the convention: return empty rather than guess. An invented suffix
		// would match an environment to a policy that does not concern it, and the promotion would go
		// into the void with nothing reporting it.
		`^v(\d+)\.(\d+)$`:     "",
		`.*-k8sunstable\..*$`: "",
		`^(?P<semver>.+)_$`:   "",
		`no-underscore`:       "",
	}

	for pattern, expected := range cases {
		got := ImagePolicy{Pattern: pattern}.Suffix()
		if got != expected {
			t.Errorf("pattern %q: suffix = %q, expected %q", pattern, got, expected)
		}
	}
}

// `Unknown` means "reconciliation in progress": confusing it with a failure would display an
// error during every normal deployment.
func TestKustomizationStateDistinguishesReconciling(t *testing.T) {
	cases := map[string]string{
		"True":    "ready",
		"False":   "failed",
		"Unknown": "reconciling",
		"":        "reconciling",
	}

	for ready, expected := range cases {
		if got := (Kustomization{Ready: ready}).State(); got != expected {
			t.Errorf("condition %q: state = %q, expected %q", ready, got, expected)
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
		t.Fatalf("policy = %+v, found = %v", policy, found)
	}
	if policy.AcceptsPrerelease() {
		t.Error("the production policy must not accept pre-releases")
	}

	if _, found := FindPolicy(policies, "testing"); found {
		t.Error("a suffix with no policy must not be found")
	}
}
