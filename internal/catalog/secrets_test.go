package catalog

import "testing"

func pipelineWithSecrets() Pipeline {
	return Pipeline{
		Kind: "build",
		Workspaces: []Workspace{
			{Name: "git-credentials", Secret: &SecretSource{ConfigKey: "pipeline.gitSecret"}},
			{Name: "dockerconfig", Secret: &SecretSource{ConfigKey: "pipeline.registrySecret"}},
			{Name: "shared-data", VolumeClaimTemplate: &VolumeClaimTemplate{Storage: "1Gi"}},
		},
	}
}

func TestSecretNamesComeFromConfiguration(t *testing.T) {
	resolved, err := pipelineWithSecrets().ResolveNames(map[string]string{
		"pipeline.gitSecret":      "git-credentials-test",
		"pipeline.registrySecret": "regcred-vcarroy",
	})
	if err != nil {
		t.Fatalf("resolution: %v", err)
	}

	if got := resolved.Workspaces[0].Secret.SecretName; got != "git-credentials-test" {
		t.Errorf("git secret %q", got)
	}
	if got := resolved.Workspaces[1].Secret.SecretName; got != "regcred-vcarroy" {
		t.Errorf("registry secret %q", got)
	}
	// A workspace with no secret passes through untouched.
	if resolved.Workspaces[2].VolumeClaimTemplate == nil {
		t.Error("the volume was lost during resolution")
	}
}

// A literal name stays literal: catalogues that use no configuration key keep
// working as they are.
func TestALiteralNameIsPreserved(t *testing.T) {
	pipeline := Pipeline{
		Kind:       "promote",
		Workspaces: []Workspace{{Name: "dockerconfig", Secret: &SecretSource{SecretName: "regcred"}}},
	}

	resolved, err := pipeline.ResolveNames(map[string]string{})
	if err != nil {
		t.Fatalf("resolution: %v", err)
	}
	if got := resolved.Workspaces[0].Secret.SecretName; got != "regcred" {
		t.Errorf("literal name altered: %q", got)
	}
}

// A missing key refuses the run rather than mounting an empty secret: Tekton would
// fail when starting the pod, with a message that does not name the cause.
func TestAMissingKeyRefusesTheRun(t *testing.T) {
	for _, config := range []map[string]string{
		{},
		{"pipeline.gitSecret": ""},
		{"pipeline.registrySecret": "regcred"},
	} {
		if _, err := pipelineWithSecrets().ResolveNames(config); err == nil {
			t.Errorf("config %v: resolution should fail", config)
		}
	}
}

// Resolution must not write into the shared catalogue, which is read once at
// start-up and serves every run. Without a copy, the second run would see the
// first run's resolved name as if it were literal — and a configuration switch
// would have no effect until restart.
func TestResolutionDoesNotContaminateTheCatalogue(t *testing.T) {
	pipeline := pipelineWithSecrets()

	if _, err := pipeline.ResolveNames(map[string]string{
		"pipeline.gitSecret":      "git-credentials-test",
		"pipeline.registrySecret": "regcred-vcarroy",
	}); err != nil {
		t.Fatalf("resolution: %v", err)
	}

	if pipeline.Workspaces[0].Secret.SecretName != "" {
		t.Errorf("the catalogue was modified: %q", pipeline.Workspaces[0].Secret.SecretName)
	}
	if pipeline.Workspaces[0].Secret.ConfigKey != "pipeline.gitSecret" {
		t.Errorf("the original key was lost: %q", pipeline.Workspaces[0].Secret.ConfigKey)
	}
}

// The failure that killed runs: no image pull secret, while every pipeline uses
// images from a private project. The global pod template must be inherited by any
// entry that declares none.
func TestThePullSecretIsInheritedFromTheCatalogue(t *testing.T) {
	c := &Catalog{
		PodTemplate: &PodTemplate{ImagePullSecretsConfigKey: "pipeline.imagePullSecret"},
		Pipelines: []Pipeline{
			{Kind: "build"},
			// An entry declaring its own wins: a pipeline may pull from a different registry.
			{Kind: "promote", PodTemplate: &PodTemplate{ImagePullSecrets: []string{"autre-secret"}}},
		},
	}

	build, _ := c.Find("build")
	if build.PodTemplate == nil || build.PodTemplate.ImagePullSecretsConfigKey != "pipeline.imagePullSecret" {
		t.Fatalf("podTemplate not inherited: %+v", build.PodTemplate)
	}

	resolved, err := build.ResolveNames(map[string]string{"pipeline.imagePullSecret": "regcred"})
	if err != nil {
		t.Fatalf("resolution: %v", err)
	}
	if got := resolved.PodTemplate.ImagePullSecrets; len(got) != 1 || got[0] != "regcred" {
		t.Errorf("resolved pull secret: %v", got)
	}

	promote, _ := c.Find("promote")
	if got := promote.PodTemplate.ImagePullSecrets; len(got) != 1 || got[0] != "autre-secret" {
		t.Errorf("the declaring entry must win: %v", got)
	}
}

// Resolving the pod template does not contaminate the shared catalogue, for the
// same reason as the workspaces.
func TestResolvingThePodTemplateDoesNotContaminate(t *testing.T) {
	c := &Catalog{
		PodTemplate: &PodTemplate{ImagePullSecretsConfigKey: "pipeline.imagePullSecret"},
		Pipelines:   []Pipeline{{Kind: "build"}},
	}

	build, _ := c.Find("build")
	if _, err := build.ResolveNames(map[string]string{"pipeline.imagePullSecret": "regcred"}); err != nil {
		t.Fatalf("resolution: %v", err)
	}

	if len(c.PodTemplate.ImagePullSecrets) != 0 {
		t.Errorf("the catalogue was modified: %v", c.PodTemplate.ImagePullSecrets)
	}
	if c.PodTemplate.ImagePullSecretsConfigKey == "" {
		t.Error("the original key was lost")
	}
}

// A missing key refuses the run rather than letting a pod start without its
// secret, which would fail with TaskRunImagePullFailed without naming the cause.
func TestAMissingPullSecretKeyRefusesTheRun(t *testing.T) {
	c := &Catalog{
		PodTemplate: &PodTemplate{ImagePullSecretsConfigKey: "pipeline.imagePullSecret"},
		Pipelines:   []Pipeline{{Kind: "build"}},
	}
	build, _ := c.Find("build")

	if _, err := build.ResolveNames(map[string]string{}); err == nil {
		t.Error("resolution should fail on a missing key")
	}
}

// A config map name resolves like a secret's, and for the same reason: a test instance reads
// another policy than the real one, and the name is all that distinguishes them.
func TestAConfigMapNameComesFromConfiguration(t *testing.T) {
	pipeline := Pipeline{
		Kind: "security-audit",
		Workspaces: []Workspace{
			{Name: "policies", ConfigMap: &ConfigMapSource{ConfigKey: "security.policies"}},
			{Name: "rules", ConfigMap: &ConfigMapSource{Name: "semgrep-rules"}},
		},
	}

	resolved, err := pipeline.ResolveNames(map[string]string{"security.policies": "policies-staging"})
	if err != nil {
		t.Fatalf("resolution: %v", err)
	}

	if got := resolved.Workspaces[0].ConfigMap.Name; got != "policies-staging" {
		t.Errorf("policies = %q", got)
	}
	if got := resolved.Workspaces[0].ConfigMap.ConfigKey; got != "" {
		t.Errorf("the key survived resolution: %q", got)
	}
	// A literal name stays literal.
	if got := resolved.Workspaces[1].ConfigMap.Name; got != "semgrep-rules" {
		t.Errorf("rules = %q", got)
	}
}

func TestAMissingConfigMapKeyIsRefused(t *testing.T) {
	pipeline := Pipeline{
		Kind:       "security-audit",
		Workspaces: []Workspace{{Name: "policies", ConfigMap: &ConfigMapSource{ConfigKey: "security.policies"}}},
	}

	if _, err := pipeline.ResolveNames(map[string]string{}); err == nil {
		t.Error("a missing key was accepted")
	}
}

// Resolution must not write into the shared catalogue: the second run would read the first run's
// resolved name as if it were literal.
func TestConfigMapResolutionDoesNotContaminateTheCatalogue(t *testing.T) {
	pipeline := Pipeline{
		Kind:       "security-audit",
		Workspaces: []Workspace{{Name: "policies", ConfigMap: &ConfigMapSource{ConfigKey: "security.policies"}}},
	}

	if _, err := pipeline.ResolveNames(map[string]string{"security.policies": "policies-staging"}); err != nil {
		t.Fatal(err)
	}
	if got := pipeline.Workspaces[0].ConfigMap.ConfigKey; got != "security.policies" {
		t.Errorf("the catalogue was modified: key = %q", got)
	}
}
