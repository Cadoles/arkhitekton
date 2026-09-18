package worker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
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
		deployKey: "arkhitekton-project-mse-git",
		registry:  "regcred-mse",
	})

	settings, err := scheduler.settingsFor(context.Background(), jobOf(7))
	if err != nil {
		t.Fatalf("réglages: %v", err)
	}

	if settings["pipeline.gitSecret"] != "arkhitekton-project-mse-git" {
		t.Errorf("clé git = %q", settings["pipeline.gitSecret"])
	}
	if settings["pipeline.registrySecret"] != "regcred-mse" {
		t.Errorf("secret de registre = %q", settings["pipeline.registrySecret"])
	}
	if settings["tekton.namespace"] != "mse-tekton-pipelines" {
		t.Errorf("le reste de la configuration a été perdu: %v", settings)
	}
}

// Les deux sont lus **séparément** : un projet qui a sa clé sans désigner de
// registre pousse là où poussent les autres. Les traiter ensemble ferait perdre
// le secret de registre du déploiement au motif que le projet n'en nomme pas.
func TestUnProjetSansRegistreDesignePousseLaOuPoussentLesAutres(t *testing.T) {
	scheduler := schedulerWith(&fakeProjectSecrets{
		deployKey: "arkhitekton-project-mse-git",
	})

	settings, err := scheduler.settingsFor(context.Background(), jobOf(7))
	if err != nil {
		t.Fatalf("réglages: %v", err)
	}

	if settings["pipeline.gitSecret"] != "arkhitekton-project-mse-git" {
		t.Errorf("clé git = %q", settings["pipeline.gitSecret"])
	}
	if settings["pipeline.registrySecret"] != "regcred" {
		t.Errorf("secret de registre = %q — le projet n'en désigne aucun",
			settings["pipeline.registrySecret"])
	}
}

/*
 * Le refus de construire sans clé propre.
 *
 * Il remplace un repli sur une clé **partagée**, retiré parce qu'une clé unique
 * ajoutée à plusieurs dépôts fait de sa révocation une panne pour tous à la fois,
 * et ne dit rien de qui a cloné quoi.
 */

// Sans clé active, le run ne part pas — et le message nomme le geste qui manque.
//
// Sans lui, le clone échouerait dans le pipeline sur une erreur SSH, très loin de
// sa cause : personne ne ferait le lien avec une clé jamais collée dans la forge.
func TestUnProjetSansCleActiveNeConstruitPas(t *testing.T) {
	scheduler := schedulerWith(&fakeProjectSecrets{registry: "regcred-mse"})

	_, err := scheduler.settingsFor(context.Background(), jobOf(7))
	if err == nil {
		t.Fatal("un projet sans clé de déploiement a pu partir")
	}
	for _, expected := range []string{"clé de déploiement", "dépôt"} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("message %q — il doit mentionner %q", err, expected)
		}
	}
}

// La carte partagée ne doit **jamais** être écrite : elle sert à tous les runs,
// et y écrire ferait porter au job suivant le secret du précédent — un dépôt
// construit avec la clé d'un autre client, sans qu'aucune erreur ne le signale.
func TestLaConfigurationPartageeNestJamaisEcrite(t *testing.T) {
	scheduler := schedulerWith(&fakeProjectSecrets{deployKey: "clef-du-projet"})

	_, _ = scheduler.settingsFor(context.Background(), jobOf(7))

	if scheduler.config["pipeline.gitSecret"] != "git-credentials" {
		t.Errorf("configuration partagée altérée: %v — le job suivant hériterait",
			scheduler.config)
	}
}

// Un job sans projet emploie les secrets du déploiement. C'est le cas des jobs
// antérieurs à la migration 0035, et il ne doit rien casser : ils sont figés en
// file, et rien ne leur rattachera un projet rétroactivement.
func TestUnJobSansProjetEmploieLesSecretsDuDeploiement(t *testing.T) {
	scheduler := schedulerWith(&fakeProjectSecrets{deployKey: "clef-du-projet"})

	settings, err := scheduler.settingsFor(context.Background(), store.Job{ID: 1})
	if err != nil {
		t.Fatalf("réglages: %v", err)
	}

	if settings["pipeline.gitSecret"] != "git-credentials" {
		t.Errorf("clé git = %q", settings["pipeline.gitSecret"])
	}
}

// Une lecture en échec ne se déguise pas en clé absente.
//
// La distinction est ce qui évite d'envoyer quelqu'un activer une clé qui existe
// déjà : la base est muette une seconde, pas la forge. La reprise couvre le cas,
// et le plafond de tentatives conclut si elle persiste.
func TestUnEchecDeLectureNeSeDeguisePasEnCleAbsente(t *testing.T) {
	muette := errors.New("base muette")
	scheduler := schedulerWith(&fakeProjectSecrets{err: muette})

	_, err := scheduler.settingsFor(context.Background(), jobOf(7))
	if !errors.Is(err, muette) {
		t.Fatalf("erreur = %v, attendue celle de la base", err)
	}
	if strings.Contains(err.Error(), "coller") {
		t.Errorf("message %q — il envoie activer une clé qui existe peut-être", err)
	}
}
