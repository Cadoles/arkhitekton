package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"strings"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plugin"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/secrets"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Les réglages des plugins, aux deux portées.
 *
 * Le registre vit dans le binaire : cet écran ne découvre rien, il montre ce que
 * cette version sait faire et ce que l'exploitation en a décidé. C'est la
 * conséquence directe du mécanisme retenu par doc 12 §12.5 — un registre à la
 * compilation, pas de chargement à l'exécution.
 *
 * # Ce que ce handler ne rend jamais
 *
 * La valeur d'un champ sensible. Elle est scellée en base et n'en ressort que
 * pour l'appel sortant qui l'emploie, dans le worker. L'écran n'en connaît que la
 * **présence** — un champ rempli affiche « enregistrée », vide affiche « absente »
 * — et c'est ce qui rend la saisie d'une clé d'API depuis un navigateur
 * acceptable : la relire ne suffit pas à la voler.
 *
 * Le corollaire est une règle d'écriture : un champ sensible **laissé vide est
 * conservé**, pas effacé. Sans elle, un formulaire qui n'affiche pas la clé la
 * renverrait vide, et le premier enregistrement du moindre autre champ
 * l'effacerait.
 */

// PluginStore est ce que les réglages de plugins attendent de la base.
type PluginStore interface {
	PluginSettingsAll(ctx context.Context) (map[string]store.PluginSettings, error)
	SetPluginSettings(ctx context.Context, entry store.PluginSettings, actor string) error
	ProjectPluginSettings(ctx context.Context, projectID int64) (map[string]store.PluginSettings, error)
	SetProjectPluginSettings(ctx context.Context, projectID int64, entry store.PluginSettings, actor string) error
	ClearProjectPluginSettings(ctx context.Context, projectID int64, pluginID string) error
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// PluginsHandler expose le registre et ses réglages.
type PluginsHandler struct {
	store PluginStore
	// cipher scelle les champs sensibles. Nil quand aucune clé n'est configurée :
	// la lecture reste possible, l'écriture d'un champ sensible est refusée avec
	// un message qui nomme la variable manquante. Refuser tout l'écran punirait un
	// déploiement qui n'a encore aucun secret à protéger.
	cipher *secrets.Cipher
	logger *slog.Logger
}

func NewPluginsHandler(plugins PluginStore, cipher *secrets.Cipher, logger *slog.Logger) *PluginsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &PluginsHandler{store: plugins, cipher: cipher, logger: logger}
}

// fieldPayload décrit un champ à l'écran qui le saisit.
type fieldPayload struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	Help     string `json:"help,omitempty"`
	Required bool   `json:"required,omitempty"`
	Secret   bool   `json:"secret,omitempty"`
	// Options ferme le jeu des valeurs acceptées, quand il l'est. L'écran en fait
	// une liste plutôt qu'une saisie libre : le refus arrive alors avant l'envoi,
	// et non depuis la forge au moment le moins utile.
	Options     []string `json:"options,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
}

// pluginPayload est un plugin et son état à une portée.
type pluginPayload struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// KindLabel nomme le point d'extension en français. Rendu par le serveur
	// plutôt que traduit côté navigateur : la table des genres vit dans le
	// registre, et la recopier ferait diverger les deux au premier ajout.
	KindLabel  string         `json:"kind_label"`
	Summary    string         `json:"summary"`
	PerProject bool           `json:"per_project"`
	Fields     []fieldPayload `json:"fields"`
	// Enabled est l'état effectif à cette portée.
	Enabled bool `json:"enabled"`
	// Config ne porte que les champs **non sensibles**. Les autres se lisent dans
	// `secrets_set`.
	Config map[string]string `json:"config"`
	// SecretsSet nomme les champs sensibles qui portent une valeur. Leur nom, pas
	// leur contenu : c'est de quoi afficher « enregistrée » sans rien divulguer.
	SecretsSet []string `json:"secrets_set"`
	// Configured dit si le scellement est possible. Faux, l'écran doit désactiver
	// les champs sensibles plutôt que de laisser saisir une valeur qui sera
	// refusée à l'enregistrement.
	Configured bool   `json:"secrets_configured"`
	UpdatedBy  string `json:"updated_by,omitempty"`
}

// List rend le registre et les réglages de l'instance.
//
// Le registre **et** les réglages ensemble : l'écran n'a aucun usage de l'un sans
// l'autre, et deux appels laisseraient un instant où il affiche des plugins sans
// savoir lesquels sont actifs.
func (h *PluginsHandler) List(w http.ResponseWriter, r *http.Request) {
	settings, err := h.store.PluginSettingsAll(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des plugins impossible", "")
		return
	}

	payload := make([]pluginPayload, 0, len(plugin.Registry()))
	for _, descriptor := range plugin.Registry() {
		entry := settings[descriptor.ID]
		payload = append(payload, h.toPayload(descriptor, plugin.ScopeGlobal, entry, entry.Enabled))
	}

	writeJSON(w, http.StatusOK, map[string]any{"plugins": payload})
}

// toPayload compose la vue d'un plugin, sans jamais y mettre de valeur sensible.
func (h *PluginsHandler) toPayload(
	descriptor plugin.Descriptor, scope plugin.Scope,
	entry store.PluginSettings, enabled bool,
) pluginPayload {
	fields := descriptor.Fields(scope)
	rendered := make([]fieldPayload, 0, len(fields))
	for _, field := range fields {
		rendered = append(rendered, fieldPayload{
			Name: field.Name, Label: field.Label, Type: string(field.Type),
			Help: field.Help, Required: field.Required, Secret: field.Secret,
			Placeholder: field.Placeholder,
		})
	}

	config := map[string]string{}
	secretsSet := []string{}
	for name, value := range entry.Config {
		if descriptor.IsSecret(scope, name) {
			if value != "" {
				secretsSet = append(secretsSet, name)
			}
			continue
		}
		config[name] = value
	}

	return pluginPayload{
		ID: descriptor.ID, Kind: string(descriptor.Kind), Label: descriptor.Label,
		KindLabel: plugin.KindLabel[descriptor.Kind], Summary: descriptor.Summary,
		PerProject: descriptor.PerProject, Fields: rendered,
		Enabled: enabled, Config: config, SecretsSet: secretsSet,
		Configured: h.cipher.Configured(), UpdatedBy: entry.UpdatedBy,
	}
}

// pluginRequest est ce qu'un formulaire envoie.
type pluginRequest struct {
	Enabled bool              `json:"enabled"`
	Config  map[string]string `json:"config"`
	// ClearedSecrets nomme les champs sensibles à effacer.
	//
	// Explicite, parce qu'un champ sensible vide veut dire « inchangé » : sans ce
	// geste distinct, effacer une clé serait impossible — et la confondre avec un
	// champ non rempli l'effacerait à chaque enregistrement.
	ClearedSecrets []string `json:"cleared_secrets,omitempty"`
}

// Set enregistre les réglages de l'instance pour un plugin.
func (h *PluginsHandler) Set(w http.ResponseWriter, r *http.Request) {
	descriptor, request, ok := h.decode(w, r)
	if !ok {
		return
	}

	existing, err := h.store.PluginSettingsAll(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des plugins impossible", "")
		return
	}

	config, ok := h.merge(w, descriptor, plugin.ScopeGlobal, request,
		existing[descriptor.ID].Config, nil)
	if !ok {
		return
	}

	identity, _ := IdentityFrom(r.Context())
	entry := store.PluginSettings{
		PluginID: descriptor.ID, Enabled: request.Enabled, Config: config,
	}
	if err := h.store.SetPluginSettings(r.Context(), entry, identity.Subject); err != nil {
		h.logger.Error("enregistrement d'un plugin", "plugin", descriptor.ID, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Enregistrement impossible", "")
		return
	}

	// Le journal porte l'activation, jamais la configuration : elle contient des
	// champs sensibles, et un journal d'audit se lit avec moins de précautions
	// qu'une base — c'est précisément son but.
	h.audit(r, "plugin.settings", descriptor.ID, map[string]any{"enabled": request.Enabled})

	writeJSON(w, http.StatusOK, h.toPayload(descriptor, plugin.ScopeGlobal, entry, entry.Enabled))
}

// ProjectList rend les plugins réglables pour un projet, et leur état effectif.
func (h *PluginsHandler) ProjectList(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}

	global, err := h.store.PluginSettingsAll(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des plugins impossible", "")
		return
	}
	own, err := h.store.ProjectPluginSettings(r.Context(), project.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des plugins du projet impossible", "")
		return
	}

	payload := make([]pluginPayload, 0, len(plugin.Registry()))
	for _, descriptor := range plugin.Registry() {
		if !descriptor.PerProject {
			// Ce qui vaut nécessairement pour toute l'instance n'apparaît pas :
			// un interrupteur qui ne ferait rien est pire qu'une absence.
			continue
		}
		if !global[descriptor.ID].Enabled {
			// Éteint sur l'instance : le projet ne peut pas le rallumer, et le
			// montrer inviterait à essayer.
			continue
		}

		entry, declared := own[descriptor.ID]
		// L'absence de décision vaut héritage, et non refus. C'est ce qui fait
		// qu'un projet déclaré avant ces tables continue de recevoir ce qu'il
		// recevait — son bon de livraison, son attestation.
		enabled := true
		if declared {
			enabled = entry.Enabled
		}

		view := h.toPayload(descriptor, plugin.ScopeProject, entry, enabled)
		view.UpdatedBy = entry.UpdatedBy
		payload = append(payload, view)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"plugins": payload,
		"project": project.Slug,
	})
}

// SetProject enregistre la décision d'un projet sur un plugin.
func (h *PluginsHandler) SetProject(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}
	descriptor, request, ok := h.decode(w, r)
	if !ok {
		return
	}
	if !descriptor.PerProject {
		writeProblem(w, http.StatusConflict, "Plugin non réglable par projet",
			"« "+descriptor.Label+" » vaut pour toute l'instance : ce qu'il porte "+
				"n'est pas une propriété d'un projet.")
		return
	}

	global, err := h.store.PluginSettingsAll(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des plugins impossible", "")
		return
	}
	if !global[descriptor.ID].Enabled {
		writeProblem(w, http.StatusConflict, "Plugin désactivé sur l'instance",
			"Un projet ne peut pas rallumer ce que l'instance a éteint : le "+
				"déploiement ne le porte pas, et chaque appel échouerait.")
		return
	}

	own, err := h.store.ProjectPluginSettings(r.Context(), project.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des plugins du projet impossible", "")
		return
	}

	// La validation se fait contre la valeur **résolue** : un projet qui laisse
	// l'URL vide parce que l'instance la fournit ne doit pas être refusé.
	config, ok := h.merge(w, descriptor, plugin.ScopeProject, request,
		own[descriptor.ID].Config, global[descriptor.ID].Config)
	if !ok {
		return
	}

	identity, _ := IdentityFrom(r.Context())
	entry := store.PluginSettings{
		PluginID: descriptor.ID, Enabled: request.Enabled, Config: config,
	}
	if err := h.store.SetProjectPluginSettings(r.Context(), project.ID, entry, identity.Subject); err != nil {
		h.logger.Error("enregistrement d'un plugin de projet",
			"plugin", descriptor.ID, "projet", project.Slug, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Enregistrement impossible", "")
		return
	}

	h.audit(r, "plugin.project.settings", project.Slug+"/"+descriptor.ID,
		map[string]any{"enabled": request.Enabled})

	writeJSON(w, http.StatusOK, h.toPayload(descriptor, plugin.ScopeProject, entry, entry.Enabled))
}

// ClearProject rend un projet à l'héritage de l'instance.
//
// Distinct d'une désactivation : « je n'ai rien de particulier à dire » n'est pas
// « non ». Les confondre ferait qu'un projet, une fois écarté du réglage commun,
// ne pourrait plus jamais le suivre à nouveau.
func (h *PluginsHandler) ClearProject(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}
	pluginID := strings.TrimSpace(r.PathValue("plugin"))
	descriptor, err := plugin.Get(pluginID)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "Plugin inconnu", err.Error())
		return
	}

	if err := h.store.ClearProjectPluginSettings(r.Context(), project.ID, descriptor.ID); err != nil {
		h.logger.Error("retrait d'un plugin de projet",
			"plugin", descriptor.ID, "projet", project.Slug, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Retrait impossible", "")
		return
	}

	h.audit(r, "plugin.project.clear", project.Slug+"/"+descriptor.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"plugin": descriptor.ID, "inherited": true})
}

// decode lit le plugin visé et le corps de la requête.
func (h *PluginsHandler) decode(w http.ResponseWriter, r *http.Request) (plugin.Descriptor, pluginRequest, bool) {
	descriptor, err := plugin.Get(strings.TrimSpace(r.PathValue("plugin")))
	if err != nil {
		writeProblem(w, http.StatusNotFound, "Plugin inconnu", err.Error())
		return plugin.Descriptor{}, pluginRequest{}, false
	}

	var request pluginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Requête illisible", "")
		return plugin.Descriptor{}, pluginRequest{}, false
	}
	return descriptor, request, true
}

// merge compose la configuration à écrire à partir de la saisie et de l'existant.
//
// Trois règles, et chacune corrige une façon de perdre une valeur :
//
//   - un champ sensible **absent de la saisie est conservé** : l'écran ne le
//     reçoit jamais, donc ne peut pas le renvoyer ;
//   - un champ sensible **explicitement effacé** l'est, ce qui demande un geste
//     distinct plutôt qu'un champ laissé vide ;
//   - un champ sensible **fourni** est scellé, jamais écrit tel quel.
func (h *PluginsHandler) merge(
	w http.ResponseWriter, descriptor plugin.Descriptor, scope plugin.Scope,
	request pluginRequest, existing, inherited map[string]string,
) (map[string]string, bool) {
	saisie := plugin.Values{}
	maps.Copy(saisie, request.Config)

	if err := descriptor.Validate(scope, saisie, plugin.Values(inherited)); err != nil {
		writeProblem(w, http.StatusBadRequest, "Réglages invalides", err.Error())
		return nil, false
	}

	config := map[string]string{}
	// Les champs sensibles déjà en base sont repris tels quels — scellés, sans
	// être ouverts : les rouvrir pour les resceller ferait passer du clair dans
	// un processus qui n'en a pas besoin.
	for name, value := range existing {
		if descriptor.IsSecret(scope, name) {
			config[name] = value
		}
	}
	for _, name := range request.ClearedSecrets {
		delete(config, name)
	}

	for name, value := range saisie {
		if !descriptor.IsSecret(scope, name) {
			config[name] = value
			continue
		}
		if strings.TrimSpace(value) == "" {
			// Vide vaut « inchangé ». L'effacement passe par `cleared_secrets`.
			continue
		}
		if !h.cipher.Configured() {
			writeProblem(w, http.StatusConflict, "Scellement non configuré",
				"SECRETS_KEY n'est pas renseignée : le dashboard refuse d'écrire "+
					"un champ sensible en clair. La produire avec "+
					"« openssl rand -base64 32 », puis la déclarer dans le déploiement.")
			return nil, false
		}
		sealed, err := h.cipher.Seal(value)
		if err != nil {
			h.logger.Error("scellement d'un réglage",
				"plugin", descriptor.ID, "champ", name, "error", err)
			writeProblem(w, http.StatusInternalServerError, "Scellement impossible", "")
			return nil, false
		}
		config[name] = sealed
	}

	return config, true
}

// project résout le projet du chemin.
func (h *PluginsHandler) project(w http.ResponseWriter, r *http.Request) (store.Project, bool) {
	slug := strings.TrimSpace(r.PathValue("project"))
	project, err := h.store.ProjectBySlug(r.Context(), slug)
	if err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeProblem(w, http.StatusNotFound, "Projet inconnu", "")
			return store.Project{}, false
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture du projet impossible", "")
		return store.Project{}, false
	}
	return project, true
}

func (h *PluginsHandler) audit(r *http.Request, action, target string, payload map[string]any) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		return
	}
	if err := h.store.Audit(r.Context(), identity.Subject, action, target, payload); err != nil {
		h.logger.Error("journal d'audit", "action", action, "target", target, "error", err)
	}
}
