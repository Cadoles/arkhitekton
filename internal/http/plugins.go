package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/secrets"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The plugins' settings, at both scopes.
 *
 * The registry lives in the binary: this screen discovers nothing, it shows what this version can
 * do and what operations decided about it. It is the direct consequence of the mechanism chosen —
 * a registry at compile time, no loading at run time.
 *
 * # What this handler never returns
 *
 * A sensitive field's value. It is sealed in the database and comes out only for the outgoing
 * call that uses it, in the worker. The screen knows only its **presence** — a filled field
 * displays "recorded", an empty one displays "absent" — and that is what makes typing an API key
 * from a browser acceptable: reading it back is not enough to steal it.
 *
 * The corollary is a writing rule: a sensitive field **left empty is kept**, not erased. Without
 * it, a form that does not display the key would send it back empty, and the first save of any
 * other field would erase it.
 */

// PluginStore is what plugin settings need from the database.
type PluginStore interface {
	PluginSettingsAll(ctx context.Context) (map[string]store.PluginSettings, error)
	SetPluginSettings(ctx context.Context, entry store.PluginSettings, actor string) error
	ProjectPluginSettings(ctx context.Context, projectID int64) (map[string]store.PluginSettings, error)
	SetProjectPluginSettings(ctx context.Context, projectID int64, entry store.PluginSettings, actor string) error
	ClearProjectPluginSettings(ctx context.Context, projectID int64, pluginID string) error
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// PluginsHandler exposes the registry and its settings.
type PluginsHandler struct {
	store PluginStore
	// cipher seals the sensitive fields. Nil when no key is configured: reading stays
	// possible, writing a sensitive field is refused with a message naming the missing
	// variable. Refusing the whole screen would punish a deployment that has no secret to
	// protect yet.
	cipher *secrets.Cipher
	logger *slog.Logger
}

func NewPluginsHandler(plugins PluginStore, cipher *secrets.Cipher, logger *slog.Logger) *PluginsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &PluginsHandler{store: plugins, cipher: cipher, logger: logger}
}

// fieldPayload describes a field to the screen that types it in.
type fieldPayload struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	Help     string `json:"help,omitempty"`
	Required bool   `json:"required,omitempty"`
	Secret   bool   `json:"secret,omitempty"`
	// Options closes the set of accepted values, where it is closed. The screen makes a list
	// of it rather than a free-text field: the refusal then arrives before sending, and not
	// from the forge at the least useful moment.
	Options     []string `json:"options,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
}

// capabilityPayload is one capability a plugin brings.
type capabilityPayload struct {
	Kind      string `json:"kind"`
	KindLabel string `json:"kind_label"`
	Name      string `json:"name"`
	Label     string `json:"label"`
}

func capabilitiesOf(descriptor plugin.Descriptor) []capabilityPayload {
	payload := make([]capabilityPayload, 0, len(descriptor.Capabilities))
	for _, header := range descriptor.Capabilities {
		payload = append(payload, capabilityPayload{
			Kind: string(header.Kind), KindLabel: capability.KindLabel[header.Kind],
			Name: header.Name, Label: header.Label,
		})
	}
	return payload
}

// pluginPayload is a plugin and its state at one scope.
type pluginPayload struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Capabilities say what the plugin brings, labels included: the table of kinds lives in the
	// registry, and copying it into the browser would let the two diverge at the first addition.
	Capabilities []capabilityPayload `json:"capabilities"`
	Summary      string              `json:"summary"`
	PerProject   bool                `json:"per_project"`
	Fields       []fieldPayload      `json:"fields"`
	// Enabled is the effective state at this scope.
	Enabled bool `json:"enabled"`
	// Config carries only the non-sensitive fields. The others are read from `secrets_set`.
	Config map[string]string `json:"config"`
	// SecretsSet names the sensitive fields that carry a value. Their names, not their
	// content: enough to display "saved" without disclosing anything.
	SecretsSet []string `json:"secrets_set"`
	// Configured says whether sealing is possible. False, the screen must disable the
	// sensitive fields rather than let someone type a value that will be refused on save.
	Configured bool   `json:"secrets_configured"`
	UpdatedBy  string `json:"updated_by,omitempty"`
}

// List returns the registry and the instance's settings.
//
// The registry and the settings together: the screen has no use for one without the
// other, and two calls would leave a moment where it shows plugins without knowing which
// are active.
func (h *PluginsHandler) List(w http.ResponseWriter, r *http.Request) {
	settings, err := h.store.PluginSettingsAll(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the plugins", "")
		return
	}

	payload := make([]pluginPayload, 0, len(plugin.Registry()))
	for _, descriptor := range plugin.Registry() {
		entry := settings[descriptor.ID]
		payload = append(payload, h.toPayload(descriptor, plugin.ScopeGlobal, entry, entry.Enabled))
	}

	writeJSON(w, http.StatusOK, map[string]any{"plugins": payload})
}

// toPayload composes a plugin's view, never putting a sensitive value in it.
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
		ID: descriptor.ID, Label: descriptor.Label,
		Capabilities: capabilitiesOf(descriptor), Summary: descriptor.Summary,
		PerProject: descriptor.PerProject, Fields: rendered,
		Enabled: enabled, Config: config, SecretsSet: secretsSet,
		Configured: h.cipher.Configured(), UpdatedBy: entry.UpdatedBy,
	}
}

// pluginRequest is what a form sends.
type pluginRequest struct {
	Enabled bool              `json:"enabled"`
	Config  map[string]string `json:"config"`
	// ClearedSecrets names the sensitive fields to clear.
	//
	// Explicit, because an empty sensitive field means "unchanged": without that distinct
	// gesture, clearing a key would be impossible — and confusing it with an unfilled field
	// would clear it on every save.
	ClearedSecrets []string `json:"cleared_secrets,omitempty"`
}

// Set records the instance's settings for a plugin.
func (h *PluginsHandler) Set(w http.ResponseWriter, r *http.Request) {
	descriptor, request, ok := h.decode(w, r)
	if !ok {
		return
	}

	existing, err := h.store.PluginSettingsAll(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the plugins", "")
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
		h.logger.Error("recording a plugin", "plugin", descriptor.ID, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Enregistrement impossible", "")
		return
	}

	// The log carries whether it is enabled, never the configuration: that contains sensitive
	// fields, and an audit log is read with fewer precautions than a database — which is
	// precisely its purpose.
	h.audit(r, "plugin.settings", descriptor.ID, map[string]any{"enabled": request.Enabled})

	writeJSON(w, http.StatusOK, h.toPayload(descriptor, plugin.ScopeGlobal, entry, entry.Enabled))
}

// ProjectList returns the plugins a project can set, and their effective state.
func (h *PluginsHandler) ProjectList(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}

	global, err := h.store.PluginSettingsAll(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the plugins", "")
		return
	}
	own, err := h.store.ProjectPluginSettings(r.Context(), project.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the project's plugins", "")
		return
	}

	payload := make([]pluginPayload, 0, len(plugin.Registry()))
	for _, descriptor := range plugin.Registry() {
		if !descriptor.PerProject {
			// What necessarily holds for the whole instance does not appear: a switch that would do
			// nothing is worse than an absence.
			continue
		}
		if !global[descriptor.ID].Enabled {
			// Switched off on the instance: the project cannot switch it back on, and showing it
			// would invite trying.
			continue
		}

		entry, declared := own[descriptor.ID]
		// The absence of a decision means inheritance, not refusal. That is what keeps a project
		// declared before these tables receiving what it used to receive.
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

// SetProject records a project's decision about a plugin.
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
		writeProblem(w, http.StatusConflict, "Plugin not settable per project",
			"“"+descriptor.Label+"” applies to the whole instance: what it carries "+
				"is not a property of a project.")
		return
	}

	global, err := h.store.PluginSettingsAll(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the plugins", "")
		return
	}
	if !global[descriptor.ID].Enabled {
		writeProblem(w, http.StatusConflict, "Plugin disabled on the instance",
			"A project cannot switch back on what the instance switched off: the "+
				"deployment does not carry it, and every call would fail.")
		return
	}

	own, err := h.store.ProjectPluginSettings(r.Context(), project.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the project's plugins", "")
		return
	}

	// Validation is against the resolved value: a project leaving the URL empty because the
	// instance provides it must not be refused.
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
		h.logger.Error("recording a project plugin",
			"plugin", descriptor.ID, "project", project.Slug, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Enregistrement impossible", "")
		return
	}

	h.audit(r, "plugin.project.settings", project.Slug+"/"+descriptor.ID,
		map[string]any{"enabled": request.Enabled})

	writeJSON(w, http.StatusOK, h.toPayload(descriptor, plugin.ScopeProject, entry, entry.Enabled))
}

// ClearProject returns a project to inheriting the instance's setting.
//
// Distinct from disabling: "I have nothing particular to say" is not "no". Confusing them
// would mean a project, once departed from the common setting, could never follow it
// again.
func (h *PluginsHandler) ClearProject(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}
	pluginID := strings.TrimSpace(r.PathValue("plugin"))
	descriptor, err := plugin.Get(pluginID)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "Unknown plugin", err.Error())
		return
	}

	if err := h.store.ClearProjectPluginSettings(r.Context(), project.ID, descriptor.ID); err != nil {
		h.logger.Error("removing a project plugin",
			"plugin", descriptor.ID, "project", project.Slug, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Retrait impossible", "")
		return
	}

	h.audit(r, "plugin.project.clear", project.Slug+"/"+descriptor.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"plugin": descriptor.ID, "inherited": true})
}

// decode reads the plugin targeted and the request's body.
func (h *PluginsHandler) decode(w http.ResponseWriter, r *http.Request) (plugin.Descriptor, pluginRequest, bool) {
	descriptor, err := plugin.Get(strings.TrimSpace(r.PathValue("plugin")))
	if err != nil {
		writeProblem(w, http.StatusNotFound, "Unknown plugin", err.Error())
		return plugin.Descriptor{}, pluginRequest{}, false
	}

	var request pluginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request", "")
		return plugin.Descriptor{}, pluginRequest{}, false
	}
	return descriptor, request, true
}

// merge composes the configuration to write from the input and what exists.
//
// Three rules, each correcting a way of losing a value:
//
//   - a sensitive field absent from the input is kept: the screen never receives it, so it
//     cannot send it back;
//   - a sensitive field explicitly cleared is cleared, which requires a distinct gesture
//     rather than a field left empty;
//   - a sensitive field supplied is sealed, never written as is.
func (h *PluginsHandler) merge(
	w http.ResponseWriter, descriptor plugin.Descriptor, scope plugin.Scope,
	request pluginRequest, existing, inherited map[string]string,
) (map[string]string, bool) {
	saisie := plugin.Values{}
	maps.Copy(saisie, request.Config)

	if err := descriptor.Validate(scope, saisie, plugin.Values(inherited)); err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid settings", err.Error())
		return nil, false
	}

	config := map[string]string{}
	// Sensitive fields already stored are carried over as they are — sealed, without being
	// opened: reopening them to reseal would move plaintext through a process that does not
	// need it.
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
			// Empty means "unchanged". Clearing goes through `cleared_secrets`.
			continue
		}
		if !h.cipher.Configured() {
			writeProblem(w, http.StatusConflict, "Sealing not configured",
				"SECRETS_KEY is not set: the dashboard refuses to write "+
					"a sensitive field in clear text. Produce it with "+
					"“openssl rand -base64 32”, then declare it in the deployment.")
			return nil, false
		}
		sealed, err := h.cipher.Seal(value)
		if err != nil {
			h.logger.Error("sealing a setting",
				"plugin", descriptor.ID, "champ", name, "error", err)
			writeProblem(w, http.StatusInternalServerError, "Scellement impossible", "")
			return nil, false
		}
		config[name] = sealed
	}

	return config, true
}

// project resolves the project named in the path.
func (h *PluginsHandler) project(w http.ResponseWriter, r *http.Request) (store.Project, bool) {
	slug := strings.TrimSpace(r.PathValue("project"))
	project, err := h.store.ProjectBySlug(r.Context(), slug)
	if err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeProblem(w, http.StatusNotFound, "Unknown project", "")
			return store.Project{}, false
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot read the project", "")
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
