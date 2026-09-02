package main

import (
	"fmt"
	"log/slog"
	"strings"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/config"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plugin"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/redmine"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/secrets"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/worker"
)

/*
 * Le pont entre la configuration du déploiement et les réglages de plugins.
 *
 * Le même geste que `SeedProjects` avant lui, et pour la même raison : ce qui
 * vivait dans des variables d'environnement se règle désormais depuis
 * l'interface, et la base doit décrire ce que le dashboard fait **réellement**
 * plutôt que ce qu'un administrateur aura pensé à y ressaisir.
 *
 * Posé une fois. Ensuite la base fait foi : un réglage corrigé dans l'interface
 * ne se fait pas écraser par un redémarrage, et une variable d'environnement
 * oubliée dans le déploiement n'y revient pas.
 */

// Les identifiants nommés ici plutôt qu'écrits en clair aux points d'usage : une
// faute de frappe produirait un réglage qu'aucun plugin ne relit, et l'écran
// dirait « enregistré » sur une valeur morte.
const (
	trackerPluginID  = "redmine"
	forgePluginID    = "gitea"
	deployerPluginID = "flux"
)

// pluginIDs énumère les plugins embarqués, pour l'amorçage des lignes manquantes.
func pluginIDs() []string {
	registry := plugin.Registry()
	ids := make([]string, 0, len(registry))
	for _, descriptor := range registry {
		ids = append(ids, descriptor.ID)
	}
	return ids
}

// openCipher construit le chiffreur des réglages sensibles.
//
// Son absence n'empêche pas de démarrer, et c'est délibéré : un déploiement qui
// n'a encore aucun secret à protéger n'a pas à être puni, et le message serait lu
// loin de la personne qui peut agir. Le refus a lieu à l'écriture, dans l'écran
// où le champ se saisit.
//
// Une clé **mal formée** est en revanche un refus de démarrer : elle dit qu'on a
// cru configurer le scellement, et laisser passer ferait écrire en clair ce qu'on
// pensait protégé.
func openCipher(cfg config.Config, logger *slog.Logger) (*secrets.Cipher, error) {
	if !cfg.Secrets.Configured() {
		logger.Warn("scellement des réglages non configuré",
			"raison", "SECRETS_KEY absente",
			"consequence", "les champs sensibles des plugins ne pourront pas être enregistrés",
			"remede", "openssl rand -base64 32")
		return nil, nil
	}

	cipher, err := secrets.New(cfg.Secrets.Key, cfg.Secrets.RetiredKeys...)
	if err != nil {
		return nil, fmt.Errorf("scellement des réglages: %w", err)
	}
	if len(cfg.Secrets.RetiredKeys) > 0 {
		logger.Info("rotation de clé en cours",
			"retirees", len(cfg.Secrets.RetiredKeys),
			"note", "les valeurs se re-scellent au fil des écritures")
	}
	return cipher, nil
}

// configuredPlugins déduit les réglages de l'instance de la configuration.
//
// Seuls les plugins dont la configuration dit quelque chose sont posés. Les
// autres — l'attestation, le bon de livraison — n'ont aucun réglage : leurs
// lignes naissent activées par `EnsurePlugins`, ce qui préserve exactement le
// comportement d'avant, où ils s'appliquaient sans qu'on ait à le demander.
func configuredPlugins(cfg config.Config, cipher *secrets.Cipher, logger *slog.Logger) ([]store.PluginSettings, error) {
	var settings []store.PluginSettings

	tracker := map[string]string{}
	if url := strings.TrimSpace(cfg.Tracker.URL); url != "" {
		tracker["url"] = url
	}
	if key := strings.TrimSpace(cfg.Tracker.APIKey); key != "" {
		sealed, err := seal(cipher, key, "la clé d'API du suivi")
		if err != nil {
			return nil, err
		}
		tracker["api_key"] = sealed
	}
	if len(tracker) > 0 {
		settings = append(settings, store.PluginSettings{
			PluginID: trackerPluginID, Enabled: true, Config: tracker,
		})
	}

	forge := map[string]string{}
	if url := strings.TrimSpace(cfg.Forge.URL); url != "" {
		forge["url"] = url
	}
	if token := strings.TrimSpace(cfg.Forge.Token); token != "" {
		sealed, err := seal(cipher, token, "le jeton de la forge")
		if err != nil {
			return nil, err
		}
		forge["token"] = sealed
	}
	if len(forge) > 0 {
		settings = append(settings, store.PluginSettings{
			PluginID: forgePluginID, Enabled: true, Config: forge,
		})
	}

	if namespace := strings.TrimSpace(cfg.Flux.Namespace); namespace != "" {
		settings = append(settings, store.PluginSettings{
			PluginID: deployerPluginID, Enabled: true,
			Config: map[string]string{"namespace": namespace},
		})
	}

	// Les statuts de revue accompagnent le suivi, mais **par projet** : le
	// vocabulaire appartient au client, et deux clients n'ont pas les mêmes. Ils
	// ne sont donc pas posés globalement — l'amorçage les laisse vides, et le
	// projet les déclare. La valeur par défaut du code continue de s'appliquer en
	// attendant.
	if len(cfg.Tracker.ReviewStatuses) > 0 && cfg.Tracker.Configured() {
		logger.Info("statuts de revue à redéclarer par projet",
			"statuts", cfg.Tracker.ReviewStatuses,
			"raison", "le vocabulaire appartient au client, pas à l'instance")
	}

	return settings, nil
}

// trackerConnector construit un client de suivi pour une connexion résolue.
//
// Le délai d'appel reste une valeur du déploiement : il borne l'effet d'une
// lenteur du suivi sur le worker, ce qui est une propriété du dashboard et non du
// client. Le reste — serveur, clé, projet — vient des réglages.
func trackerConnector(cfg config.Config) worker.TrackerConnector {
	return func(connection worker.TrackerConnection) (worker.TrackerReader, error) {
		if strings.TrimSpace(connection.URL) == "" || strings.TrimSpace(connection.APIKey) == "" {
			// Nommé plutôt que laissé au client : « URL invalide » enverrait
			// chercher une faute de frappe là où c'est un réglage jamais saisi.
			return nil, fmt.Errorf(
				"suivi non configuré: renseigner l'URL et la clé d'API du plugin %q, "+
					"sur l'instance ou sur le projet", trackerPluginID)
		}
		return redmine.New(redmine.Options{
			URL: connection.URL, APIKey: connection.APIKey,
			Timeout: cfg.Tracker.Timeout,
		})
	}
}

// seal scelle une valeur, ou refuse l'amorçage faute de clé.
//
// Refuser plutôt qu'écrire en clair, et refuser plutôt que sauter la valeur : un
// amorçage qui pose l'URL sans la clé produirait un suivi configuré à moitié, qui
// échoue à chaque appel avec un message d'authentification. L'étape est marquée
// comme faite et ne se rejouera pas — d'où l'échec franc.
func seal(cipher *secrets.Cipher, value, what string) (string, error) {
	if !cipher.Configured() {
		return "", fmt.Errorf(
			"SECRETS_KEY est requise pour amorcer %s: la déclarer "+
				"(openssl rand -base64 32) puis relancer la migration", what)
	}
	sealed, err := cipher.Seal(value)
	if err != nil {
		return "", fmt.Errorf("scellement de %s: %w", what, err)
	}
	return sealed, nil
}
