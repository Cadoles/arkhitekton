package worker

import (
	"context"
	"errors"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * La superposition des secrets d'un projet sur ceux du déploiement.
 *
 * C'est l'endroit où un run part avec le mauvais secret **sans rien dire** : le
 * pipeline réussit à cloner ou échoue au push, et le message désigne le dépôt ou
 * le registre, jamais le réglage qui l'a envoyé là. D'où des tests sur une
 * fonction de six lignes.
 */

type fakeProjectSecrets struct {
	deployKey string
	registry  string
	err       error
}

func (f *fakeProjectSecrets) DeployKeySecret(context.Context, int64) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	return f.deployKey, f.deployKey != "", nil
}

func (f *fakeProjectSecrets) RegistrySecret(context.Context, int64) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	return f.registry, f.registry != "", nil
}

func schedulerWith(keys ProjectKeys) *Scheduler {
	scheduler := &Scheduler{
		config: map[string]string{
			"pipeline.gitSecret":      "git-credentials",
			"pipeline.registrySecret": "regcred",
			"tekton.namespace":        "mse-tekton-pipelines",
		},
		logger: quietLogger(),
	}
	if keys != nil {
		scheduler.deployKeys = keys
	}
	return scheduler
}

func jobOf(projectID int64) store.Job {
	return store.Job{ID: 1, ProjectID: &projectID}
}

// Les deux secrets d'un projet remplacent ceux du déploiement, et le reste de la
// configuration passe intact.
func TestLesSecretsDuProjetRemplacentCeuxDuDeploiement(t *testing.T) {
	scheduler := schedulerWith(&fakeProjectSecrets{
		deployKey: "mse-dashboard-project-mse-git",
		registry:  "regcred-mse",
	})

	settings := scheduler.settingsFor(context.Background(), jobOf(7))

	if settings["pipeline.gitSecret"] != "mse-dashboard-project-mse-git" {
		t.Errorf("clé git = %q", settings["pipeline.gitSecret"])
	}
	if settings["pipeline.registrySecret"] != "regcred-mse" {
		t.Errorf("secret de registre = %q", settings["pipeline.registrySecret"])
	}
	if settings["tekton.namespace"] != "mse-tekton-pipelines" {
		t.Errorf("le reste de la configuration a été perdu: %v", settings)
	}
}

// Les deux sont lus **séparément** : un projet peut avoir sa clé sans désigner de
// registre, et l'inverse. Les traiter ensemble ferait perdre l'un quand l'autre
// manque, et le run partirait avec un secret partagé qu'on croirait remplacé.
func TestUnSeulSecretDesigneNemporterPasLAutre(t *testing.T) {
	scheduler := schedulerWith(&fakeProjectSecrets{
		deployKey: "mse-dashboard-project-mse-git",
	})

	settings := scheduler.settingsFor(context.Background(), jobOf(7))

	if settings["pipeline.gitSecret"] != "mse-dashboard-project-mse-git" {
		t.Errorf("clé git = %q", settings["pipeline.gitSecret"])
	}
	if settings["pipeline.registrySecret"] != "regcred" {
		t.Errorf("secret de registre = %q — le projet n'en désigne aucun",
			settings["pipeline.registrySecret"])
	}
}

// La carte partagée ne doit **jamais** être écrite : elle sert à tous les runs,
// et y écrire ferait porter au job suivant le secret du précédent — un dépôt
// construit avec la clé d'un autre client, sans qu'aucune erreur ne le signale.
func TestLaConfigurationPartageeNestJamaisEcrite(t *testing.T) {
	scheduler := schedulerWith(&fakeProjectSecrets{deployKey: "clef-du-projet"})

	_ = scheduler.settingsFor(context.Background(), jobOf(7))

	if scheduler.config["pipeline.gitSecret"] != "git-credentials" {
		t.Errorf("configuration partagée altérée: %v — le job suivant hériterait",
			scheduler.config)
	}
}

// Un job sans projet emploie les secrets du déploiement. C'est le cas des jobs
// antérieurs à la migration 0035, et il ne doit rien casser.
func TestUnJobSansProjetEmploieLesSecretsPartages(t *testing.T) {
	scheduler := schedulerWith(&fakeProjectSecrets{deployKey: "clef-du-projet"})

	settings := scheduler.settingsFor(context.Background(), store.Job{ID: 1})

	if settings["pipeline.gitSecret"] != "git-credentials" {
		t.Errorf("clé git = %q", settings["pipeline.gitSecret"])
	}
}

// Un échec de lecture fait partir le run avec les secrets partagés plutôt que de
// l'empêcher : c'est l'état d'avant le lot G, et refuser de lancer sur une base
// momentanément muette serait pire que de construire avec la clé commune.
func TestUnEchecDeLectureRetombeSurLesSecretsPartages(t *testing.T) {
	scheduler := schedulerWith(&fakeProjectSecrets{err: errors.New("base muette")})

	settings := scheduler.settingsFor(context.Background(), jobOf(7))

	if settings["pipeline.gitSecret"] != "git-credentials" {
		t.Errorf("clé git = %q", settings["pipeline.gitSecret"])
	}
	if settings["pipeline.registrySecret"] != "regcred" {
		t.Errorf("secret de registre = %q", settings["pipeline.registrySecret"])
	}
}
