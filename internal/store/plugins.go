package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

/*
 * Les réglages des plugins, aux deux portées.
 *
 * Le registre vit dans le binaire (`internal/plugin`) : ce magasin ne sait pas
 * quels plugins existent, et ne cherche pas à le savoir. Il porte ce que
 * l'exploitation en a décidé — activé ou non, réglé ainsi — pour des
 * identifiants que l'appelant lui donne.
 *
 * La séparation est volontaire. Une table qui énumérerait les plugins connus
 * demanderait une migration à chaque ajout, alors qu'ajouter un plugin est déjà
 * une reconstruction du binaire : la base apprendrait une seconde fois ce que le
 * code sait déjà, avec une occasion de plus de diverger.
 */

// PluginSettings porte l'état d'un plugin à une portée.
type PluginSettings struct {
	PluginID string
	Enabled  bool
	// Config est une carte plate de chaînes. Les champs sensibles y sont
	// **scellés** : ce magasin les transporte sans les lire, et c'est la couche
	// au-dessus qui ouvre et referme.
	Config    map[string]string
	UpdatedAt time.Time
	// UpdatedBy est vide pour ce que l'amorçage a posé. La distinction dit qu'une
	// valeur vient de la configuration du déploiement, et non d'une décision
	// humaine qu'on pourrait aller questionner.
	UpdatedBy string
}

// EnsurePlugins pose les lignes manquantes des plugins embarqués.
//
// Appelé au démarrage, avec les identifiants du registre. Les lignes naissent
// **activées** : chaque plugin embarqué décrit quelque chose que le dashboard
// fait déjà, et naître désactivé arrêterait au premier déploiement des
// mécanismes qui fonctionnaient la veille.
//
// Idempotent, et sans écrasement : un plugin désactivé à la main le reste après
// un redémarrage, sinon le réglage ne tiendrait pas plus longtemps qu'un pod.
func (db *DB) EnsurePlugins(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	const query = `
		insert into plugin_settings (plugin_id, enabled)
		select unnest($1::text[]), true
		on conflict (plugin_id) do nothing`

	if _, err := db.pool.Exec(ctx, query, ids); err != nil {
		return fmt.Errorf("amorçage des plugins: %w", err)
	}
	return nil
}

// PluginSettingsAll rend les réglages globaux, par identifiant.
//
// Une carte et non une tranche : l'appelant croise avec le registre du binaire,
// qui fait foi sur ce qui existe et dans quel ordre l'afficher. Rendre une
// tranche ordonnée par la base l'inviterait à s'en servir comme catalogue, alors
// qu'elle ne connaît que ce qui a déjà été réglé.
func (db *DB) PluginSettingsAll(ctx context.Context) (map[string]PluginSettings, error) {
	const query = `
		select plugin_id, enabled, config, updated_at, updated_by
		from plugin_settings`

	rows, err := db.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("lecture des plugins: %w", err)
	}
	defer rows.Close()

	settings := map[string]PluginSettings{}
	for rows.Next() {
		var entry PluginSettings
		if err := rows.Scan(&entry.PluginID, &entry.Enabled, &entry.Config,
			&entry.UpdatedAt, &entry.UpdatedBy); err != nil {
			return nil, fmt.Errorf("lecture d'un plugin: %w", err)
		}
		settings[entry.PluginID] = entry
	}
	return settings, rows.Err()
}

// SetPluginSettings enregistre les réglages globaux d'un plugin.
func (db *DB) SetPluginSettings(ctx context.Context, entry PluginSettings, actor string) error {
	const query = `
		insert into plugin_settings (plugin_id, enabled, config, updated_at, updated_by)
		values ($1, $2, $3, now(), $4)
		on conflict (plugin_id) do update
		set enabled    = excluded.enabled,
		    config     = excluded.config,
		    updated_at = excluded.updated_at,
		    updated_by = excluded.updated_by`

	if _, err := db.pool.Exec(ctx, query, entry.PluginID, entry.Enabled,
		orEmptyConfig(entry.Config), actor); err != nil {
		return fmt.Errorf("enregistrement du plugin %q: %w", entry.PluginID, err)
	}
	return nil
}

// ProjectPluginSettings rend les décisions explicites d'un projet.
//
// N'y figurent que les plugins sur lesquels le projet s'est prononcé. L'absence
// d'une entrée vaut **héritage du global**, et non refus : la traiter comme un
// refus désactiverait d'un coup, pour tous les projets déclarés avant que ces
// tables existent, ce qu'ils reçoivent aujourd'hui.
func (db *DB) ProjectPluginSettings(ctx context.Context, projectID int64) (map[string]PluginSettings, error) {
	const query = `
		select plugin_id, enabled, config, updated_at, updated_by
		from project_plugins
		where project_id = $1`

	rows, err := db.pool.Query(ctx, query, projectID)
	if err != nil {
		return nil, fmt.Errorf("lecture des plugins du projet: %w", err)
	}
	defer rows.Close()

	settings := map[string]PluginSettings{}
	for rows.Next() {
		var entry PluginSettings
		if err := rows.Scan(&entry.PluginID, &entry.Enabled, &entry.Config,
			&entry.UpdatedAt, &entry.UpdatedBy); err != nil {
			return nil, fmt.Errorf("lecture d'un plugin de projet: %w", err)
		}
		settings[entry.PluginID] = entry
	}
	return settings, rows.Err()
}

// SetProjectPluginSettings enregistre la décision d'un projet sur un plugin.
func (db *DB) SetProjectPluginSettings(ctx context.Context, projectID int64,
	entry PluginSettings, actor string,
) error {
	const query = `
		insert into project_plugins (project_id, plugin_id, enabled, config, updated_at, updated_by)
		values ($1, $2, $3, $4, now(), $5)
		on conflict (project_id, plugin_id) do update
		set enabled    = excluded.enabled,
		    config     = excluded.config,
		    updated_at = excluded.updated_at,
		    updated_by = excluded.updated_by`

	_, err := db.pool.Exec(ctx, query, projectID, entry.PluginID, entry.Enabled,
		orEmptyConfig(entry.Config), actor)
	if err != nil {
		return fmt.Errorf("enregistrement du plugin %q du projet %d: %w",
			entry.PluginID, projectID, err)
	}
	return nil
}

// ClearProjectPluginSettings retire la décision d'un projet.
//
// Le projet revient à l'héritage du global. Distinct d'une désactivation :
// « je n'ai rien de particulier à dire » n'est pas « non », et les confondre
// ferait qu'un projet ne pourrait plus jamais suivre le réglage de l'instance
// une fois qu'il s'en est écarté.
func (db *DB) ClearProjectPluginSettings(ctx context.Context, projectID int64, pluginID string) error {
	const query = `delete from project_plugins where project_id = $1 and plugin_id = $2`

	if _, err := db.pool.Exec(ctx, query, projectID, pluginID); err != nil {
		return fmt.Errorf("retrait du plugin %q du projet %d: %w", pluginID, projectID, err)
	}
	return nil
}

// ErrPluginDisabled signale un plugin qu'on interroge alors qu'il est éteint.
var ErrPluginDisabled = errors.New("plugin désactivé")

// ResolvedPlugin rend les réglages effectifs d'un plugin pour un projet.
//
// Le calcul est fait ici, en une seule requête, plutôt que par l'appelant à
// partir de deux lectures : c'est la superposition dont dépend chaque appel
// sortant, et deux endroits qui la calculent finiraient par la calculer
// différemment.
//
// La configuration rendue reste **scellée** pour les champs sensibles. Ouvrir
// demande la clé, et ce magasin ne l'a pas — délibérément : il ne doit pas
// pouvoir rendre du clair, même par erreur d'appel.
func (db *DB) ResolvedPlugin(ctx context.Context, projectID int64, pluginID string) (PluginSettings, error) {
	const query = `
		select g.enabled,
		       g.config,
		       p.enabled,
		       p.config
		from plugin_settings g
		left join project_plugins p
		       on p.plugin_id = g.plugin_id and p.project_id = $2
		where g.plugin_id = $1`

	var (
		globalEnabled  bool
		globalConfig   map[string]string
		projectEnabled *bool
		projectConfig  map[string]string
	)
	err := db.pool.QueryRow(ctx, query, pluginID, projectID).
		Scan(&globalEnabled, &globalConfig, &projectEnabled, &projectConfig)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Aucun réglage global : le plugin n'a jamais été amorcé sur cette
		// instance. Traité comme éteint, ce qu'il est de fait.
		return PluginSettings{PluginID: pluginID}, fmt.Errorf("%w: %s",
			ErrPluginDisabled, pluginID)
	case err != nil:
		return PluginSettings{}, fmt.Errorf("lecture du plugin %q: %w", pluginID, err)
	}

	// Le projet ne peut pas rallumer ce que l'instance a éteint : un plugin
	// désactivé globalement l'est parce que le déploiement ne le porte pas, et
	// un projet qui le forcerait échouerait à chaque appel.
	enabled := globalEnabled
	if globalEnabled && projectEnabled != nil {
		enabled = *projectEnabled
	}

	resolved := PluginSettings{
		PluginID: pluginID,
		Enabled:  enabled,
		Config:   overlayConfig(globalConfig, projectConfig),
	}
	if !enabled {
		return resolved, fmt.Errorf("%w: %s", ErrPluginDisabled, pluginID)
	}
	return resolved, nil
}

// overlayConfig superpose les réglages d'un projet à ceux de l'instance.
//
// Une valeur vide côté projet n'efface pas celle de l'instance : elle veut dire
// « rien de particulier ». Le confondre avec un effacement ferait perdre une URL
// au premier enregistrement d'un formulaire qui ne l'affichait pas — le cas est
// certain pour les champs secrets, que l'API ne renvoie jamais.
func overlayConfig(global, project map[string]string) map[string]string {
	resolved := make(map[string]string, len(global)+len(project))
	for name, value := range global {
		resolved[name] = value
	}
	for name, value := range project {
		if value == "" {
			continue
		}
		resolved[name] = value
	}
	return resolved
}

// seedPlugins fait passer la configuration des plugins de l'environnement à la
// base, une seule fois.
//
// Deux gestes, et le second est celui qui compte. Les réglages de l'instance
// naissent de la configuration du déploiement — l'URL et la clé d'un suivi, la
// forge, le namespace de Flux. Puis **chaque projet déjà rattaché à un suivi
// reçoit son propre réglage**, avec l'identifiant qu'il portait déjà.
//
// Sans ce second geste, un déploiement en place verrait sa synchronisation
// s'arrêter le jour où le code lit les réglages plutôt que la configuration : la
// connexion serait là, mais aucun projet ne dirait quoi synchroniser. Une panne
// muette, exactement celle qui a motivé l'étape `trackers`.
//
// Rien n'est écrasé : `on conflict do nothing`. Un réglage déjà posé — par
// l'interface, ou par un passage antérieur — l'emporte sur ce que la
// configuration suggère.
func (db *DB) seedPlugins(ctx context.Context, settings []PluginSettings, trackerID string) (int, error) {
	posed := 0

	for _, entry := range settings {
		const query = `
			insert into plugin_settings (plugin_id, enabled, config)
			values ($1, $2, $3)
			on conflict (plugin_id) do nothing`

		tag, err := db.pool.Exec(ctx, query, entry.PluginID, entry.Enabled,
			orEmptyConfig(entry.Config))
		if err != nil {
			return posed, fmt.Errorf("amorçage du plugin %q: %w", entry.PluginID, err)
		}
		posed += int(tag.RowsAffected())
	}

	if strings.TrimSpace(trackerID) == "" {
		return posed, nil
	}

	// Le rattachement des projets. `tracker_project` reste la source : la colonne
	// existe, elle est juste, et la recopier dans le réglage est ce qui permet à
	// un second projet de désigner un autre serveur sans que le premier bouge.
	const attach = `
		insert into project_plugins (project_id, plugin_id, enabled, config)
		select id, $1, true, jsonb_build_object('project', tracker_project)
		from projects
		where btrim(tracker_project) <> ''
		on conflict (project_id, plugin_id) do nothing`

	tag, err := db.pool.Exec(ctx, attach, trackerID)
	if err != nil {
		return posed, fmt.Errorf("rattachement des projets au suivi: %w", err)
	}
	return posed + int(tag.RowsAffected()), nil
}

// orEmptyConfig évite d'écrire `null` dans une colonne déclarée `not null`.
func orEmptyConfig(config map[string]string) map[string]string {
	if config == nil {
		return map[string]string{}
	}
	return config
}
