package mse

import (
	"context"
	"errors"
	"strings"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plugin"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Ce que les pipelines du MSE ont besoin de savoir, par projet.
 *
 * Le noyau exposait ces valeurs à l'ordonnanceur sous des clés qui nomment le MSE
 * — `forge.mseKustom.repoUrl`, `mse.overlayLocation` — lues dans sa configuration
 * (doc 16 §16.18, couture oubliée de §16.7). Elles viennent désormais des réglages
 * du plugin, où un projet peut les surcharger : deux projets ne clonent pas la
 * même kustomisation.
 */

// pipelineFields associe un réglage du plugin à la clé que le catalogue référence.
//
// Deux vocabulaires, et c'est délibéré : le catalogue nomme ce dont un pipeline a
// besoin, le plugin ce qu'un administrateur règle. Les confondre obligerait
// l'écran d'administration à afficher `forge.mseKustom.repoUrl`.
var pipelineFields = map[string]string{
	"kustomRepoUrl":   "forge.mseKustom.repoUrl",
	"kustomRevision":  "forge.mseKustom.revision",
	"e2eRepoUrl":      "forge.e2e.repoUrl",
	"overlayLocation": "mse.overlayLocation",
}

// SettingsReader rend les réglages effectifs d'un plugin pour un projet.
type SettingsReader interface {
	ResolvedPlugin(ctx context.Context, projectID int64, pluginID string) (store.PluginSettings, error)
}

// PipelineSettings rend les valeurs de configuration qu'un projet ajoute aux
// paramètres de ses pipelines.
//
// Rien quand le module est éteint pour lui : ses pipelines n'ont alors aucune
// raison de cloner une kustomisation, et un paramètre requis non couvert fait
// échouer la mise en file avec un message qui le nomme.
//
// Une valeur vide n'est pas rendue : c'est l'instance qui la fournit, et une
// chaîne vide écraserait la sienne (voir [plugin.Resolve]).
func PipelineSettings(settings SettingsReader) func(context.Context, int64) (map[string]string, error) {
	return func(ctx context.Context, projectID int64) (map[string]string, error) {
		resolved, err := settings.ResolvedPlugin(ctx, projectID, plugin.ModuleMSE)
		switch {
		case errors.Is(err, store.ErrPluginDisabled):
			return nil, nil
		case err != nil:
			return nil, err
		}

		values := make(map[string]string, len(pipelineFields))
		for field, key := range pipelineFields {
			if value := strings.TrimSpace(resolved.Config[field]); value != "" {
				values[key] = value
			}
		}
		return values, nil
	}
}

// PipelineDefaults rend les réglages globaux à amorcer depuis la configuration.
//
// L'amorçage, et non la lecture : les variables d'environnement du déploiement
// posent la première valeur, la base fait foi ensuite. Sans cela, un réglage
// modifié depuis l'administration serait repris par le déploiement suivant.
func PipelineDefaults(kustomRepoURL, kustomRevision, e2eRepoURL, overlayLocation string) map[string]string {
	defaults := map[string]string{
		"kustomRepoUrl":   kustomRepoURL,
		"kustomRevision":  kustomRevision,
		"e2eRepoUrl":      e2eRepoURL,
		"overlayLocation": overlayLocation,
	}
	for field, value := range defaults {
		if strings.TrimSpace(value) == "" {
			delete(defaults, field)
		}
	}
	return defaults
}
