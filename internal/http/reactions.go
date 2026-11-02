package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/action"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/configure"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/reaction"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

/*
 * The declared reactions, and the catalogue that makes writing them possible.
 *
 * # The references are checked here
 *
 * `internal/reaction` carries the shape; this layer carries the **references** — that a fact type
 * is produced, that a step names a catalogue kind or a registry action. It is here because it is
 * here that the registries are at hand.
 *
 * Checked when recorded and not when triggered: a faulty reaction discovered at the moment it was
 * to start shows only as an absence, and an absence is badly diagnosed — one first looks for
 * whether the fact was emitted.
 *
 * # The editor's catalogue
 *
 * The screen hard-codes no list. It asks what this version can do: the facts one can listen to,
 * with their payload's fields; the actions, with their parameters; the catalogue's kinds. A list
 * written in the interface would diverge from the binary at the first addition, and the gap would
 * show through a step one can choose and nothing runs.
 */

// AutomationStore is what the automations need from the database.
type AutomationStore interface {
	ListReactions(ctx context.Context, projectID int64) ([]reaction.Reaction, error)
	GetReaction(ctx context.Context, projectID, id int64) (reaction.Reaction, error)
	SaveReaction(ctx context.Context, entry reaction.Reaction, actor string) (int64, error)
	DeleteReaction(ctx context.Context, projectID, id int64) error
	ReleaseAutomation(ctx context.Context, projectID int64) (store.ReleaseAutomation, error)
	SaveReleaseAutomation(ctx context.Context, entry store.ReleaseAutomation, actor string) error
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	PluginSettingsAll(ctx context.Context) (map[string]store.PluginSettings, error)
	ProjectPluginSettings(ctx context.Context, projectID int64) (map[string]store.PluginSettings, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// AutomationsHandler exposes the reactions and the editor's catalogue.
type AutomationsHandler struct {
	store    AutomationStore
	catalog  catalog.Source
	registry capabilities.Source
	actions  action.Catalog
	logger   *slog.Logger
	// schedules serves the schedules; nil, their routes answer 404.
	schedules ScheduleStore
}

func NewAutomationsHandler(
	reactions AutomationStore, pipelines catalog.Source, registry capabilities.Source, logger *slog.Logger,
) *AutomationsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &AutomationsHandler{
		store: reactions, catalog: pipelines, registry: registry,
		actions: action.NewCatalog(registry.Current().Plugins()), logger: logger,
	}
}

type reactionPayload struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	EventType string            `json:"event_type"`
	Enabled   bool              `json:"enabled"`
	Match     map[string]string `json:"match"`
	Plan      plan.Plan         `json:"plan"`
	// Inert says the reaction can no longer trigger: its fact is no longer produced by this
	// version, or the plugin that produced it is switched off.
	//
	// Returned rather than the reaction deleted: a switch-off may be temporary, and deleting
	// silently would lose a configuration. The screen displays it as inert, and says so.
	Inert  bool   `json:"inert,omitempty"`
	Reason string `json:"inert_reason,omitempty"`
}

// List returns a project's reactions.
func (h *AutomationsHandler) List(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}

	reactions, err := h.store.ListReactions(r.Context(), project.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the reactions", "")
		return
	}

	live, err := h.service().LiveEventTypes(r.Context(), project.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the plugins", "")
		return
	}

	payload := make([]reactionPayload, 0, len(reactions))
	for _, entry := range reactions {
		view := reactionPayload{
			ID: entry.ID, Name: entry.Name, EventType: entry.EventType,
			Enabled: entry.Enabled, Match: orEmptyMap(entry.Match), Plan: entry.Plan,
		}
		if !slices.Contains(live, entry.EventType) {
			view.Inert = true
			view.Reason = "this fact is no longer produced: the plugin that observes it is " +
				"switched off, or this version no longer declares it"
		}
		payload = append(payload, view)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"reactions": payload,
		"project":   project.Slug,
	})
}

type reactionRequest struct {
	Name      string            `json:"name"`
	EventType string            `json:"event_type"`
	Enabled   bool              `json:"enabled"`
	Match     map[string]string `json:"match"`
	Plan      plan.Plan         `json:"plan"`
}

// Save creates or replaces a reaction.
func (h *AutomationsHandler) Save(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}
	var request reactionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request", "")
		return
	}

	// The identifier comes from the path and not from the body: a body carrying it would allow
	// moving a reaction from one project to another by editing two fields, and the URL would stop
	// saying what is being written.
	var id int64
	if raw := strings.TrimSpace(r.PathValue("reaction")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "Invalid identifier", "")
			return
		}
		id = parsed
	}

	identity, _ := IdentityFrom(r.Context())
	saved, err := h.service().SaveReaction(r.Context(), identity.Subject, project, reaction.Reaction{
		ID: id, Name: request.Name, EventType: request.EventType, Enabled: request.Enabled,
		Match: request.Match, Plan: request.Plan,
	})
	if err != nil {
		writeRefusal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": saved})
}

// Delete removes a reaction.
func (h *AutomationsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("reaction")), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid identifier", "")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	if err := h.service().DeleteReaction(r.Context(), identity.Subject, project, id); err != nil {
		writeRefusal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

/*
 * The editor's catalogue.
 */

type eventChoice struct {
	Type  string `json:"type"`
	Label string `json:"label"`
	// Source distinguishes the core from a plugin. Empty for the core.
	Source string `json:"source,omitempty"`
	// Fields lists the matchable fields, and therefore the template variables.
	Fields []string `json:"fields"`
}

type gesteChoice struct {
	Kind  string `json:"kind"`
	Ref   string `json:"ref"`
	Label string `json:"label"`
	// Summary is set only for an action: a catalogue kind is described by its label.
	Summary string         `json:"summary,omitempty"`
	Fields  []fieldPayload `json:"fields,omitempty"`
	// Produces lists what the pipeline leaves behind, in the catalogue's vocabulary. Empty
	// for an action, which produces nothing observable.
	Produces []string `json:"produces,omitempty"`
	// Source names the plugin supplying the step. Empty for the core.
	//
	// Returned for the same reason as on a fact: two steps may carry the same label and not
	// talk to the same forge.
	Source string `json:"source,omitempty"`
}

// Catalogue returns what this version can do.
//
// The screen hard-codes no list: it asks, and composes its form. A list written in the
// interface would diverge from the binary at the first addition, and the gap would show
// as a step one can choose and nothing executes.
func (h *AutomationsHandler) Catalogue(w http.ResponseWriter, r *http.Request) {
	settings, err := h.store.PluginSettingsAll(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the plugins", "")
		return
	}

	events := coreEventChoices()
	for _, descriptor := range capabilities.Of(h.registry).Plugins() {
		// A fact whose plugin is switched off does not appear in the editor: a choice that would
		// never trigger is worse than an absence.
		if !settings[descriptor.ID].Enabled {
			continue
		}
		for _, spec := range descriptor.Events {
			events = append(events, eventChoice{
				Type: descriptor.EventType(spec), Label: spec.Label,
				Source: descriptor.ID, Fields: fieldNames(spec.Payload),
			})
		}
	}

	steps := make([]gesteChoice, 0, 8)
	for _, descriptor := range h.actions.All() {
		// An action whose plugin is switched off does not appear, exactly like a fact it no
		// longer observes: a step one can choose and that will refuse to leave is worse than an
		// absence.
		if descriptor.Plugin != "" && !settings[descriptor.Plugin].Enabled {
			continue
		}
		steps = append(steps, gesteChoice{
			Kind: string(plan.GesteAction), Ref: descriptor.ID,
			Label: descriptor.Label, Summary: descriptor.Summary,
			Fields: actionFields(descriptor), Source: descriptor.Plugin,
		})
	}
	if h.catalog != nil {
		for _, kind := range catalog.Of(h.catalog).Kinds() {
			pipeline, found := catalog.Of(h.catalog).Find(kind)
			if !found {
				continue
			}
			steps = append(steps, gesteChoice{
				Kind: string(plan.GestePipeline), Ref: pipeline.Kind, Label: pipeline.Label,
				// The parameters a pipeline asks for, with their labels. They were not returned while a
				// release plan was composed in Go: nobody had to read a parameter name. Declaring the
				// build reverses that.
				Fields: pipelineFields(pipeline),
				// What the pipeline produces, so the editor knows which stage it may go into. The server
				// refuses anyway, but a form that lets one choose and then refuses is a form people learn
				// to work around.
				Produces: produces(pipeline),
			})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"events": events,
		// `gestes`, and not `steps`: the interface reads this key, and it stays French like the
		// rest of the wire format it consumes.
		"gestes": steps,
		"when":   []string{string(plan.WhenSuccess), string(plan.WhenFailure), string(plan.WhenAlways)},
		// The stage roles, and what each requires of its steps. The editor presents two blocks —
		// "Build", "Verification" — and must be able to say which accepts what without hard-coding
		// the table.
		"roles": roleChoices(),
	})
}

// roleChoice describes a stage role for the editor.
type roleChoice struct {
	Role string `json:"role"`
	// Label is the block's title in the editor.
	Label string `json:"label"`
	// Requires is the product a step must declare to enter it.
	Requires string `json:"requires"`
	// Advances says what concluding this stage does to the release.
	Advances string `json:"advances"`
}

func roleChoices() []roleChoice {
	return []roleChoice{
		{
			Role: string(plan.RoleBuild), Label: "Construction",
			Requires: string(catalog.ProductImage),
			Advances: "the release moves into verification, or fails",
		},
		{
			Role: string(plan.RoleTest), Label: "Verification",
			Requires: string(catalog.ProductResults),
			Advances: "the release becomes ready, or fails",
		},
	}
}

// pipelineFields returns the parameters a declaration may fill.
func pipelineFields(pipeline catalog.Pipeline) []fieldPayload {
	declared := pipeline.Declared()
	fields := make([]fieldPayload, 0, len(declared))
	for _, input := range declared {
		fields = append(fields, fieldPayload{
			Name: input.Name, Label: input.Label, Type: "text",
			Help: input.Help, Required: input.Required,
			Placeholder: input.Placeholder,
		})
	}
	return fields
}

func produces(pipeline catalog.Pipeline) []string {
	names := make([]string, 0, len(pipeline.Produces))
	for _, product := range pipeline.Produces {
		names = append(names, string(product))
	}
	return names
}

// coreEventChoices lists the core facts, with their matchable fields.
//
// Written here and not derived: core facts have no descriptor — they are emitted by code,
// not declared by a plugin — and giving them a registry for the sole convenience of this
// list would add a second truth to keep aligned.
func coreEventChoices() []eventChoice {
	core := configure.CoreEvents()
	choices := make([]eventChoice, 0, len(core))
	for _, event := range core {
		choices = append(choices, eventChoice{Type: event.Type, Label: event.Label, Fields: event.Fields})
	}
	return choices
}

func fieldNames(fields []capability.Field) []string {
	names := make([]string, 0, len(fields))
	for _, field := range fields {
		names = append(names, field.Name)
	}
	return names
}

func actionFields(descriptor action.Descriptor) []fieldPayload {
	fields := make([]fieldPayload, 0, len(descriptor.Fields))
	for _, field := range descriptor.Fields {
		fields = append(fields, fieldPayload{
			Name: field.Name, Label: field.Label, Type: "text", Help: field.Help,
			Required: field.Required, Secret: field.SecretRef,
			Options: field.OneOf, Placeholder: field.Placeholder,
		})
	}
	return fields
}

func (h *AutomationsHandler) project(w http.ResponseWriter, r *http.Request) (store.Project, bool) {
	project, err := h.store.ProjectBySlug(r.Context(),
		strings.TrimSpace(r.PathValue("project")))
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
