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

func TestLesNomsDeSecretsViennentDeLaConfiguration(t *testing.T) {
	resolved, err := pipelineWithSecrets().ResolveSecrets(map[string]string{
		"pipeline.gitSecret":      "git-credentials-test",
		"pipeline.registrySecret": "regcred-vcarroy",
	})
	if err != nil {
		t.Fatalf("résolution: %v", err)
	}

	if got := resolved.Workspaces[0].Secret.SecretName; got != "git-credentials-test" {
		t.Errorf("secret git %q", got)
	}
	if got := resolved.Workspaces[1].Secret.SecretName; got != "regcred-vcarroy" {
		t.Errorf("secret registre %q", got)
	}
	// Un workspace sans secret traverse sans être touché.
	if resolved.Workspaces[2].VolumeClaimTemplate == nil {
		t.Error("le volume a été perdu à la résolution")
	}
}

// Un nom littéral reste littéral : les catalogues qui n'utilisent pas de clé de
// configuration continuent de fonctionner tels quels.
func TestUnNomLitteralEstConserve(t *testing.T) {
	pipeline := Pipeline{
		Kind:       "promote",
		Workspaces: []Workspace{{Name: "dockerconfig", Secret: &SecretSource{SecretName: "regcred"}}},
	}

	resolved, err := pipeline.ResolveSecrets(map[string]string{})
	if err != nil {
		t.Fatalf("résolution: %v", err)
	}
	if got := resolved.Workspaces[0].Secret.SecretName; got != "regcred" {
		t.Errorf("nom littéral altéré: %q", got)
	}
}

// Une clé absente refuse le run plutôt que de monter un secret vide : Tekton
// échouerait au démarrage du pod sur un message qui ne désignerait pas la cause.
func TestUneCleAbsenteRefuseLeRun(t *testing.T) {
	for _, config := range []map[string]string{
		{},
		{"pipeline.gitSecret": ""},
		{"pipeline.registrySecret": "regcred"},
	} {
		if _, err := pipelineWithSecrets().ResolveSecrets(config); err == nil {
			t.Errorf("config %v: la résolution devrait échouer", config)
		}
	}
}

// La résolution ne doit pas écrire dans le catalogue partagé : celui-ci est lu
// une fois au démarrage et sert tous les runs. Sans copie, le second run verrait
// le nom résolu du premier comme s'il était littéral — et une bascule de
// configuration n'aurait plus d'effet jusqu'au redémarrage.
func TestLaResolutionNeContaminePasLeCatalogue(t *testing.T) {
	pipeline := pipelineWithSecrets()

	if _, err := pipeline.ResolveSecrets(map[string]string{
		"pipeline.gitSecret":      "git-credentials-test",
		"pipeline.registrySecret": "regcred-vcarroy",
	}); err != nil {
		t.Fatalf("résolution: %v", err)
	}

	if pipeline.Workspaces[0].Secret.SecretName != "" {
		t.Errorf("le catalogue a été modifié: %q", pipeline.Workspaces[0].Secret.SecretName)
	}
	if pipeline.Workspaces[0].Secret.ConfigKey != "pipeline.gitSecret" {
		t.Errorf("la clé d'origine a été perdue: %q", pipeline.Workspaces[0].Secret.ConfigKey)
	}
}

// Le défaut qui a fait échouer les runs : aucun secret de tirage, alors que les
// cinq pipelines emploient des images d'un projet privé. Le podTemplate global
// doit être hérité par toute entrée qui n'en déclare pas.
func TestLeSecretDeTirageEstHeriteDuCatalogue(t *testing.T) {
	c := &Catalog{
		PodTemplate: &PodTemplate{ImagePullSecretsConfigKey: "pipeline.imagePullSecret"},
		Pipelines: []Pipeline{
			{Kind: "build"},
			// Une entrée qui déclare le sien l'emporte : un pipeline peut tirer
			// depuis un registre différent des autres.
			{Kind: "promote", PodTemplate: &PodTemplate{ImagePullSecrets: []string{"autre-secret"}}},
		},
	}

	build, _ := c.Find("build")
	if build.PodTemplate == nil || build.PodTemplate.ImagePullSecretsConfigKey != "pipeline.imagePullSecret" {
		t.Fatalf("podTemplate non hérité: %+v", build.PodTemplate)
	}

	resolved, err := build.ResolveSecrets(map[string]string{"pipeline.imagePullSecret": "regcred"})
	if err != nil {
		t.Fatalf("résolution: %v", err)
	}
	if got := resolved.PodTemplate.ImagePullSecrets; len(got) != 1 || got[0] != "regcred" {
		t.Errorf("secret de tirage résolu: %v", got)
	}

	promote, _ := c.Find("promote")
	if got := promote.PodTemplate.ImagePullSecrets; len(got) != 1 || got[0] != "autre-secret" {
		t.Errorf("l'entrée déclarante doit l'emporter: %v", got)
	}
}

// La résolution du podTemplate ne contamine pas le catalogue partagé, pour la
// même raison que celle des workspaces.
func TestLaResolutionDuPodTemplateNeContaminePas(t *testing.T) {
	c := &Catalog{
		PodTemplate: &PodTemplate{ImagePullSecretsConfigKey: "pipeline.imagePullSecret"},
		Pipelines:   []Pipeline{{Kind: "build"}},
	}

	build, _ := c.Find("build")
	if _, err := build.ResolveSecrets(map[string]string{"pipeline.imagePullSecret": "regcred"}); err != nil {
		t.Fatalf("résolution: %v", err)
	}

	if len(c.PodTemplate.ImagePullSecrets) != 0 {
		t.Errorf("le catalogue a été modifié: %v", c.PodTemplate.ImagePullSecrets)
	}
	if c.PodTemplate.ImagePullSecretsConfigKey == "" {
		t.Error("la clé d'origine a été perdue")
	}
}

// Une clé absente refuse le run plutôt que de laisser partir un pod sans secret,
// qui échouerait en TaskRunImagePullFailed sans désigner la cause.
func TestUneCleDeTirageAbsenteRefuseLeRun(t *testing.T) {
	c := &Catalog{
		PodTemplate: &PodTemplate{ImagePullSecretsConfigKey: "pipeline.imagePullSecret"},
		Pipelines:   []Pipeline{{Kind: "build"}},
	}
	build, _ := c.Find("build")

	if _, err := build.ResolveSecrets(map[string]string{}); err == nil {
		t.Error("la résolution devrait échouer sur une clé absente")
	}
}
