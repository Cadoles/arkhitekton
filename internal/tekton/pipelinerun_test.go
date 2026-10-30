package tekton

import (
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
)

/*
 * The workspace bindings of a PipelineRun.
 *
 * One switch, four sources, and nothing in the compiler says a fifth was forgotten: a workspace
 * whose source the switch does not know falls through to an empty binding, which Tekton accepts
 * before failing at mount time. These tests hold each source.
 */

func TestASecretBackedWorkspaceCarriesItsName(t *testing.T) {
	binding, err := workspaceBinding(catalog.Workspace{
		Name: "git-credentials", Secret: &catalog.SecretSource{SecretName: "git"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if binding.Secret == nil || binding.Secret.SecretName != "git" {
		t.Errorf("binding = %+v", binding)
	}
}

func TestAConfigMapBackedWorkspaceCarriesItsName(t *testing.T) {
	binding, err := workspaceBinding(catalog.Workspace{
		Name: "policies", ConfigMap: &catalog.ConfigMapSource{Name: "security-policies"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if binding.ConfigMap == nil || binding.ConfigMap.Name != "security-policies" {
		t.Errorf("binding = %+v", binding)
	}
}

// An unresolved key means the configuration was never read. Binding an empty name would mount
// nothing while reporting success, and the run would fail on a policy it never saw.
func TestAnUnresolvedConfigMapIsRefused(t *testing.T) {
	_, err := workspaceBinding(catalog.Workspace{
		Name: "policies", ConfigMap: &catalog.ConfigMapSource{ConfigKey: "security.policies"},
	})
	if err == nil {
		t.Fatal("an unresolved config map was accepted")
	}
	if !strings.Contains(err.Error(), "policies") {
		t.Errorf("error = %v", err)
	}
}

func TestAWorkspaceWithNoSourceIsRefused(t *testing.T) {
	if _, err := workspaceBinding(catalog.Workspace{Name: "orphan"}); err == nil {
		t.Error("a workspace with no source was accepted")
	}
}
