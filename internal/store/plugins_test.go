package store

import (
	"context"
	"errors"
	"testing"
)

/*
 * Les réglages de plugins, et la superposition dont dépend chaque appel sortant.
 *
 * Éprouvés contre une vraie base plutôt qu'avec un faux : ce qui se joue ici est
 * une jointure et une règle de conflit, c'est-à-dire précisément ce qu'un faux ne
 * reproduit pas. Le défaut qui a motivé le harnais frontend vivait au même
 * endroit — entre un handler et PostgreSQL, hors de portée des deux.
 */

func TestLesPluginsNaissentActivesEtNeSontPasEcrases(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if err := db.EnsurePlugins(ctx, []string{"redmine", "flux"}); err != nil {
		t.Fatalf("amorçage: %v", err)
	}

	settings, err := db.PluginSettingsAll(ctx)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	// Activés à la naissance : chaque plugin embarqué décrit quelque chose que le
	// dashboard fait déjà, et naître désactivé arrêterait au premier déploiement
	// des mécanismes qui fonctionnaient la veille.
	if !settings["redmine"].Enabled || !settings["flux"].Enabled {
		t.Fatalf("réglages = %+v", settings)
	}

	// Une extinction à la main survit à un redémarrage : sinon le réglage ne
	// tiendrait pas plus longtemps qu'un pod.
	if err := db.SetPluginSettings(ctx,
		PluginSettings{PluginID: "flux", Enabled: false}, "alice"); err != nil {
		t.Fatalf("extinction: %v", err)
	}
	if err := db.EnsurePlugins(ctx, []string{"redmine", "flux"}); err != nil {
		t.Fatalf("second amorçage: %v", err)
	}

	settings, _ = db.PluginSettingsAll(ctx)
	if settings["flux"].Enabled {
		t.Error("l'amorçage a rallumé un plugin éteint à la main")
	}
	if settings["flux"].UpdatedBy != "alice" {
		t.Errorf("auteur = %q", settings["flux"].UpdatedBy)
	}
}

// La superposition : le projet l'emporte, le vide n'efface pas.
//
// Le piège que la seconde règle évite est certain : l'API ne renvoie jamais un
// champ sensible, donc un formulaire le soumet vide. Le traiter comme un
// effacement ferait perdre la clé de l'instance au premier enregistrement.
func TestLesReglagesDUnProjetSeSuperposentSansEffacer(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if err := db.EnsurePlugins(ctx, []string{"redmine"}); err != nil {
		t.Fatalf("amorçage: %v", err)
	}
	if err := db.SetPluginSettings(ctx, PluginSettings{
		PluginID: "redmine", Enabled: true,
		Config: map[string]string{"url": "https://redmine.example.fr", "api_key": "scellée"},
	}, "alice"); err != nil {
		t.Fatalf("réglage global: %v", err)
	}

	if err := db.SetProjectPluginSettings(ctx, testProjectID, PluginSettings{
		PluginID: "redmine", Enabled: true,
		// L'URL vide vaut « rien de particulier à dire », pas « efface-la ».
		Config: map[string]string{"project": "mse-dev", "url": ""},
	}, "alice"); err != nil {
		t.Fatalf("réglage de projet: %v", err)
	}

	resolved, err := db.ResolvedPlugin(ctx, testProjectID, "redmine")
	if err != nil {
		t.Fatalf("résolution: %v", err)
	}
	if resolved.Config["url"] != "https://redmine.example.fr" {
		t.Errorf("URL résolue = %q — le vide n'efface pas", resolved.Config["url"])
	}
	if resolved.Config["api_key"] != "scellée" {
		t.Errorf("clé résolue = %q", resolved.Config["api_key"])
	}
	if resolved.Config["project"] != "mse-dev" {
		t.Errorf("projet résolu = %q", resolved.Config["project"])
	}
}

// Un projet ne rallume pas ce que l'instance a éteint : le déploiement ne le
// porte pas, et chaque appel échouerait.
func TestUnProjetNeRallumePasCeQueLInstanceAEteint(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if err := db.SetPluginSettings(ctx,
		PluginSettings{PluginID: "redmine", Enabled: false}, "alice"); err != nil {
		t.Fatalf("réglage global: %v", err)
	}
	if err := db.SetProjectPluginSettings(ctx, testProjectID,
		PluginSettings{PluginID: "redmine", Enabled: true}, "alice"); err != nil {
		t.Fatalf("réglage de projet: %v", err)
	}

	_, err := db.ResolvedPlugin(ctx, testProjectID, "redmine")
	if !errors.Is(err, ErrPluginDisabled) {
		t.Errorf("résolution = %v, attendu ErrPluginDisabled", err)
	}
}

// Le retrait rend le projet à l'héritage, et n'est pas une désactivation.
//
// Les confondre ferait qu'un projet, une fois écarté du réglage commun, ne
// pourrait plus jamais le suivre à nouveau.
func TestLeRetraitRendLeProjetALHeritage(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if err := db.EnsurePlugins(ctx, []string{"bon-de-livraison"}); err != nil {
		t.Fatalf("amorçage: %v", err)
	}
	if err := db.SetProjectPluginSettings(ctx, testProjectID,
		PluginSettings{PluginID: "bon-de-livraison", Enabled: false}, "alice"); err != nil {
		t.Fatalf("extinction: %v", err)
	}
	if _, err := db.ResolvedPlugin(ctx, testProjectID, "bon-de-livraison"); !errors.Is(err, ErrPluginDisabled) {
		t.Fatalf("résolution = %v", err)
	}

	if err := db.ClearProjectPluginSettings(ctx, testProjectID, "bon-de-livraison"); err != nil {
		t.Fatalf("retrait: %v", err)
	}
	resolved, err := db.ResolvedPlugin(ctx, testProjectID, "bon-de-livraison")
	if err != nil || !resolved.Enabled {
		t.Errorf("après retrait: %+v (%v) — le projet devrait suivre l'instance", resolved, err)
	}
}

/*
 * Les pipelines qu'un projet retient.
 */

// L'absence de décision vaut **activé**.
//
// Traiter l'absence comme un refus arrêterait d'un coup les vérifications de
// tous les projets déclarés avant la table : un déploiement doit pouvoir
// appliquer une migration sans que sa chaîne de vérification s'arrête.
func TestUnPipelineNonDeclareResteActif(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	enabled, err := db.PipelineEnabled(ctx, testProjectID, "load-tests")
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if !enabled {
		t.Error("une famille non déclarée devrait rester active")
	}

	if err := db.SetProjectPipeline(ctx, testProjectID, "load-tests", false, "alice"); err != nil {
		t.Fatalf("extinction: %v", err)
	}
	if enabled, _ := db.PipelineEnabled(ctx, testProjectID, "load-tests"); enabled {
		t.Error("la famille désactivée reste active")
	}

	decisions, err := db.ProjectPipelines(ctx, testProjectID)
	if err != nil {
		t.Fatalf("lecture des décisions: %v", err)
	}
	// Seules les décisions explicites figurent : c'est ce qui permet à l'écran de
	// distinguer un choix d'un défaut, et donc de proposer de revenir au défaut.
	if len(decisions) != 1 || decisions["load-tests"].Enabled {
		t.Errorf("décisions = %+v", decisions)
	}
}

/*
 * La projection des statuts, superposée elle aussi.
 */

// Le défaut de l'instance vaut pour un projet qui n'a rien déclaré, et ses
// écarts l'emportent là où il en a.
//
// Les deux moitiés comptent. Sans le défaut, un projet neuf naîtrait sans aucune
// règle et compterait toutes ses demandes comme non commencées. Sans les écarts,
// un statut déclaré pour un client déplacerait un chiffre affiché à un autre.
func TestLaProjectionSuperposeLeDefautEtLesEcartsDuProjet(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	// « Fermé » vient de l'amorçage, sans projet.
	mapping, err := db.StatusMapping(ctx, testProjectID)
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	if mapping["Fermé"] != "done" {
		t.Fatalf("le défaut d'instance n'est pas hérité: %q", mapping["Fermé"])
	}

	// Le projet le range ailleurs.
	if err := db.SetStatusBucket(ctx, testProjectID, "Fermé", "client-test"); err != nil {
		t.Fatalf("écart: %v", err)
	}
	mapping, _ = db.StatusMapping(ctx, testProjectID)
	if mapping["Fermé"] != "client-test" {
		t.Errorf("écart du projet = %q, il devrait l'emporter", mapping["Fermé"])
	}

	// Et le défaut n'a pas bougé : un autre projet le lit inchangé. C'est le
	// point qui protège le chiffre affiché à l'autre client.
	autre, err := db.CreateProject(ctx, Project{
		Repository: "CNOUS/autre", Slug: "autre", Label: "autre",
		ForgeURL: "https://forge.example.fr", BuildContext: ".",
	})
	if err != nil {
		t.Fatalf("second projet: %v", err)
	}
	voisin, _ := db.StatusMapping(ctx, autre)
	if voisin["Fermé"] != "done" {
		t.Errorf("projection du voisin = %q — un écart a débordé", voisin["Fermé"])
	}
}

// Le curseur est propre à chaque projet.
//
// C'est la correction du défaut que le synchroniseur documentait : un curseur
// commun faisait qu'un second projet reprenait depuis un instant où il n'avait
// rien lu, et sautait son historique.
func TestChaqueProjetPorteSonPropreCurseur(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	autre, err := db.CreateProject(ctx, Project{
		Repository: "CNOUS/autre", Slug: "autre", Label: "autre",
		ForgeURL: "https://forge.example.fr", BuildContext: ".",
	})
	if err != nil {
		t.Fatalf("second projet: %v", err)
	}

	if err := db.RecordSyncSuccess(ctx, "redmine", testProjectID, "2026-08-01T00:00:00Z"); err != nil {
		t.Fatalf("curseur du premier: %v", err)
	}

	state, err := db.GetSyncState(ctx, "redmine", autre)
	if err != nil {
		t.Fatalf("état du second: %v", err)
	}
	if state.Cursor != "" {
		t.Errorf("curseur du second = %q — il aurait sauté son historique", state.Cursor)
	}

	// Et l'échec de l'un ne touche pas l'autre.
	if err := db.RecordSyncFailure(ctx, "redmine", autre, "injoignable"); err != nil {
		t.Fatalf("échec du second: %v", err)
	}
	premier, _ := db.GetSyncState(ctx, "redmine", testProjectID)
	if premier.LastError != "" {
		t.Errorf("erreur du premier = %q — elle vient de l'autre projet", premier.LastError)
	}
}
