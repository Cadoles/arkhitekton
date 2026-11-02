package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/configure"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/secrets"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
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
	registry capabilities.Source
	store    PluginStore
	// cipher seals the sensitive fields. Nil when no key is configured: reading stays
	// possible, writing a sensitive field is refused with a message naming the missing
	// variable. Refusing the whole screen would punish a deployment that has no secret to
	// protect yet.
	cipher  *secrets.Cipher
	service configure.Plugins
	logger  *slog.Logger
}

func NewPluginsHandler(
	registry capabilities.Source, plugins PluginStore, cipher *secrets.Cipher, logger *slog.Logger,
) *PluginsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &PluginsHandler{
		registry: registry, store: plugins, cipher: cipher, logger: logger,
		service: configure.Plugins{Store: plugins, Registry: registry, Cipher: cipher, Logger: logger},
	}
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

func capabilitiesOf(descriptor capability.Plugin) []capabilityPayload {
	headers := descriptor.Headers()
	payload := make([]capabilityPayload, 0, len(headers))
	for _, header := range headers {
		payload = append(payload, capabilityPayload{
			Kind: string(header.Kind), KindLabel: plugin.KindLabel[header.Kind],
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
	// Mandatory says the plugin cannot be switched off: the screen shows no switch.
	Mandatory bool           `json:"mandatory"`
	Fields    []fieldPayload `json:"fields"`
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
	// WebhookPath is where the forge delivers for this project, for a plugin that receives. Empty
	// at the instance's scope: the address names a project.
	WebhookPath string `json:"webhook_path,omitempty"`
}

// webhookPath is the address a plugin receives a project's deliveries at, as the forge is given it.
func webhookPath(descriptor capability.Plugin, slug string) string {
	if descriptor.Webhook == nil {
		return ""
	}
	return "/api/v1/hooks/" + descriptor.ID + "/projects/" + slug
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

	payload := make([]pluginPayload, 0, len(capabilities.Of(h.registry).Plugins()))
	for _, descriptor := range capabilities.Of(h.registry).Plugins() {
		entry := settings[descriptor.ID]
		payload = append(payload, h.toPayload(descriptor, plugin.ScopeGlobal, entry, entry.Enabled))
	}

	writeJSON(w, http.StatusOK, map[string]any{"plugins": payload})
}

// toPayload composes a plugin's view, never putting a sensitive value in it.
func (h *PluginsHandler) toPayload(
	descriptor capability.Plugin, scope plugin.Scope,
	entry store.PluginSettings, enabled bool,
) pluginPayload {
	fields := plugin.Fields(descriptor, scope)
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
		if plugin.IsSecret(descriptor, scope, name) {
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
		PerProject: descriptor.PerProject, Mandatory: descriptor.Mandatory, Fields: rendered,
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
	request, ok := decodePluginRequest(w, r)
	if !ok {
		return
	}
	identity, _ := IdentityFrom(r.Context())
	descriptor, entry, err := h.service.Set(r.Context(), identity.Subject, r.PathValue("plugin"), request.input())
	if err != nil {
		writeRefusal(w, err)
		return
	}
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

	payload := make([]pluginPayload, 0, len(capabilities.Of(h.registry).Plugins()))
	for _, descriptor := range capabilities.Of(h.registry).Plugins() {
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
		view.WebhookPath = webhookPath(descriptor, project.Slug)
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
	request, ok := decodePluginRequest(w, r)
	if !ok {
		return
	}
	identity, _ := IdentityFrom(r.Context())
	descriptor, entry, err := h.service.SetProject(r.Context(), identity.Subject, project, r.PathValue("plugin"), request.input())
	if err != nil {
		writeRefusal(w, err)
		return
	}
	view := h.toPayload(descriptor, plugin.ScopeProject, entry, entry.Enabled)
	view.WebhookPath = webhookPath(descriptor, project.Slug)
	writeJSON(w, http.StatusOK, view)
}

// ClearProject returns a project to inheriting the instance's setting.
func (h *PluginsHandler) ClearProject(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}
	identity, _ := IdentityFrom(r.Context())
	descriptor, err := h.service.ClearProject(r.Context(), identity.Subject, project, r.PathValue("plugin"))
	if err != nil {
		writeRefusal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plugin": descriptor.ID, "inherited": true})
}

// decodePluginRequest reads a plugin's settings from the request's body.
//
// The plugin is looked up by the service, after the body: an unknown plugin and an unreadable body
// both refuse the request, and the order changes nothing to what is written.
func decodePluginRequest(w http.ResponseWriter, r *http.Request) (pluginRequest, bool) {
	var request pluginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request", "")
		return pluginRequest{}, false
	}
	return request, true
}

func (r pluginRequest) input() configure.PluginInput {
	return configure.PluginInput{Enabled: r.Enabled, Config: r.Config, ClearedSecrets: r.ClearedSecrets}
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
