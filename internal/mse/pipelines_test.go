package mse

import (
	"context"
	"errors"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

type reglages struct {
	config   map[string]string
	disabled bool
}

func (r reglages) ResolvedPlugin(_ context.Context, _ int64, pluginID string) (store.PluginSettings, error) {
	if r.disabled {
		return store.PluginSettings{PluginID: pluginID}, store.ErrPluginDisabled
	}
	return store.PluginSettings{PluginID: pluginID, Enabled: true, Config: r.config}, nil
}

// Les réglages du plugin deviennent les valeurs que le catalogue référence, sous
// les clés qu'il nomme.
func TestLesReglagesDevisennentLesClesDuCatalogue(t *testing.T) {
	settings := PipelineSettings(reglages{config: map[string]string{
		"kustomRepoUrl":   "ssh://git@forge/vcarroy/kustom.git",
		"kustomRevision":  "main",
		"e2eRepoUrl":      "ssh://git@forge/vcarroy/e2e.git",
		"overlayLocation": "overlays/pipeline",
		// Un réglage qui ne concerne pas les pipelines : il ne doit pas fuir dans
		// leurs paramètres.
		"namespace": "flux-system",
	}})

	values, err := settings(context.Background(), 1)
	if err != nil {
		t.Fatalf("réglages: %v", err)
	}
	attendu := map[string]string{
		"forge.mseKustom.repoUrl":  "ssh://git@forge/vcarroy/kustom.git",
		"forge.mseKustom.revision": "main",
		"forge.e2e.repoUrl":        "ssh://git@forge/vcarroy/e2e.git",
		"mse.overlayLocation":      "overlays/pipeline",
	}
	if len(values) != len(attendu) {
		t.Fatalf("valeurs = %v", values)
	}
	for key, value := range attendu {
		if values[key] != value {
			t.Errorf("%s = %q, attendu %q", key, values[key], value)
		}
	}
}

// Un réglage vide n'est pas rendu : c'est l'instance qui le fournit, et une
// chaîne vide écraserait la sienne.
func TestUnReglageVideLaisseCeluiDeLInstance(t *testing.T) {
	settings := PipelineSettings(reglages{config: map[string]string{
		"kustomRepoUrl": "  ", "kustomRevision": "main",
	}})

	values, err := settings(context.Background(), 1)
	if err != nil {
		t.Fatalf("réglages: %v", err)
	}
	if _, present := values["forge.mseKustom.repoUrl"]; present {
		t.Errorf("un réglage vide a été rendu: %v", values)
	}
	if values["forge.mseKustom.revision"] != "main" {
		t.Errorf("valeurs = %v", values)
	}
}

// Module éteint pour le projet : aucune valeur. Ses pipelines n'ont pas de
// kustomisation à cloner, et le dire ici évite un run qui pointerait ailleurs.
func TestUnProjetSansLeModuleNApporteRien(t *testing.T) {
	settings := PipelineSettings(reglages{disabled: true})

	values, err := settings(context.Background(), 2)
	if err != nil || values != nil {
		t.Errorf("valeurs = %v (%v), attendu aucune", values, err)
	}
}

// Une lecture impossible remonte : un run parti avec la kustomisation d'un autre
// projet serait pire qu'un run refusé.
func TestUneLectureImpossibleRemonte(t *testing.T) {
	settings := PipelineSettings(lectureEnEchec{})

	if _, err := settings(context.Background(), 1); err == nil {
		t.Error("une lecture impossible doit remonter")
	}
}

type lectureEnEchec struct{}

func (lectureEnEchec) ResolvedPlugin(context.Context, int64, string) (store.PluginSettings, error) {
	return store.PluginSettings{}, errors.New("base injoignable")
}

// L'amorçage ne pose que ce que la configuration fournit.
func TestLAmorcageIgnoreLesValeursAbsentes(t *testing.T) {
	defaults := PipelineDefaults("ssh://git@forge/kustom.git", "", "", "overlays/pipeline")

	if len(defaults) != 2 || defaults["kustomRepoUrl"] == "" || defaults["overlayLocation"] == "" {
		t.Errorf("défauts = %v", defaults)
	}
}
