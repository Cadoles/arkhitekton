package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/action"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/reaction"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
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
	store   AutomationStore
	catalog *catalog.Catalog
	logger  *slog.Logger
}

func NewAutomationsHandler(
	reactions AutomationStore, pipelines *catalog.Catalog, logger *slog.Logger,
) *AutomationsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &AutomationsHandler{store: reactions, catalog: pipelines, logger: logger}
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

	live, err := h.liveEventTypes(r.Context(), project.ID)
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

	// The identifier comes from the path and not from the body: a body carrying it would
	// allow moving a reaction from one project to another by editing two fields, and the URL
	// would stop saying what is being written.
	var id int64
	if raw := strings.TrimSpace(r.PathValue("reaction")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "Identifiant invalide", "")
			return
		}
		id = parsed
	}

	entry := reaction.Reaction{
		ID: id, ProjectID: project.ID, Name: strings.TrimSpace(request.Name),
		EventType: strings.TrimSpace(request.EventType), Enabled: request.Enabled,
		Match: request.Match, Plan: request.Plan,
	}

	if err := entry.Validate(); err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid reaction", err.Error())
		return
	}
	if err := h.checkRefs(r.Context(), entry); err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid reaction", err.Error())
		return
	}

	identity, _ := IdentityFrom(r.Context())
	saved, err := h.store.SaveReaction(r.Context(), entry, identity.Subject)
	switch {
	case errors.Is(err, store.ErrReactionExists):
		writeProblem(w, http.StatusConflict, "Name already taken",
			"a reaction of this project already carries this name")
		return
	case errors.Is(err, store.ErrReactionNotFound):
		writeProblem(w, http.StatusNotFound, "Reaction not found", "")
		return
	case err != nil:
		h.logger.Error("recording a reaction",
			"project", project.Slug, "name", entry.Name, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Enregistrement impossible", "")
		return
	}

	// The log carries the fact listened for and whether it is enabled. Not the plan: it
	// carries templates, and an audit log is read with fewer precautions than a database.
	h.audit(r, "reaction.save", project.Slug+"/"+entry.Name, map[string]any{
		"event_type": entry.EventType, "enabled": entry.Enabled,
	})

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
		writeProblem(w, http.StatusBadRequest, "Identifiant invalide", "")
		return
	}

	if err := h.store.DeleteReaction(r.Context(), project.ID, id); err != nil {
		if errors.Is(err, store.ErrReactionNotFound) {
			writeProblem(w, http.StatusNotFound, "Reaction not found", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Suppression impossible", "")
		return
	}

	h.audit(r, "reaction.delete", project.Slug+"/"+strconv.FormatInt(id, 10), nil)
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
	for _, descriptor := range plugin.Registry() {
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
	for _, descriptor := range action.All() {
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
		for _, kind := range h.catalog.Kinds() {
			pipeline, found := h.catalog.Find(kind)
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
		"steps":  steps,
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
	return []eventChoice{
		{Type: string(event.VersionCreated), Label: "Version declared in the tracker",
			Fields: []string{"version_id", "name", "status", "tool", "external_id", "due_date"}},
		{Type: string(event.ReleaseCreated), Label: "Release declared",
			Fields: []string{"release_id", "tag", "kind", "git_ref", "version"}},
		{Type: string(event.ReleaseReleased), Label: "Release validated",
			Fields: []string{"release_id", "tag", "released_by"}},
	}
}

func fieldNames(fields []plugin.Field) []string {
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

/*
 * The reference checks.
 */

// checkRefs refuses a reaction one of whose references names nothing.
func (h *AutomationsHandler) checkRefs(ctx context.Context, entry reaction.Reaction) error {
	var problems []error

	live, err := h.liveEventTypes(ctx, entry.ProjectID)
	if err != nil {
		return err
	}
	if !slices.Contains(live, entry.EventType) {
		problems = append(problems, fmt.Errorf(
			"fact %q is produced by no active plugin of this version (known: %s)",
			entry.EventType, strings.Join(live, ", ")))
	}

	// A role on a reaction's stage makes no sense and would be ignored: it is the stages of a
	// release's plan that advance its state, and a reaction aims at no particular release.
	for index, stage := range entry.Plan.Stages {
		if stage.Role != "" {
			problems = append(problems, fmt.Errorf(
				"stage %d of a reaction carries the role %q: a role only advances "+
					"a release's automation", index+1, stage.Role))
		}
	}

	problems = append(problems, h.checkPlanRefs(ctx, entry.Plan))
	return errors.Join(problems...)
}

// checkPlanRefs refuses a plan one of whose steps names nothing executable.
//
// Shared by the reactions and a release's automation: both carry the same structure, and
// two checks would diverge at the first addition.
func (h *AutomationsHandler) checkPlanRefs(ctx context.Context, entry plan.Plan) error {
	var problems []error

	pipelines, actions := plan.Refs(entry)
	for _, ref := range pipelines {
		if h.catalog == nil {
			problems = append(problems, fmt.Errorf(
				"kind %q cannot be checked: no catalogue is loaded", ref))
			continue
		}
		if _, found := h.catalog.Find(ref); !found {
			problems = append(problems, fmt.Errorf(
				"kind %q is missing from the catalogue (declared: %s)",
				ref, strings.Join(h.catalog.Kinds(), ", ")))
		}
	}
	// The settings are read back here rather than passed from liveEventTypes: this path is a
	// save, not a display, and a second read costs less here than one more parameter to carry
	// around.
	settings, err := h.store.PluginSettingsAll(ctx)
	if err != nil {
		return err
	}
	for _, ref := range actions {
		descriptor, err := action.Get(ref)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		// A step whose plugin is switched off is refused when saved, like a fact no active plugin
		// produces. The symmetry is not aesthetic: in both cases the reaction would be declared,
		// visible, and without effect — which is what must not be suggested.
		if descriptor.Plugin != "" && !settings[descriptor.Plugin].Enabled {
			problems = append(problems, fmt.Errorf(
				"step %q comes from plugin %q, which is switched off for this instance: "+
					"it would not start", ref, descriptor.Plugin))
		}
	}

	// An action's parameters are validated against its descriptor: a malformed URL or a
	// mismatched template is corrected in front of the form, not in front of a halted chain.
	for _, stage := range entry.Stages {
		for _, step := range stage.Gestes {
			if step.Kind != plan.GesteAction {
				continue
			}
			descriptor, err := action.Get(step.Ref)
			if err != nil {
				continue
			}
			if err := descriptor.Validate(step.Params); err != nil {
				problems = append(problems, err)
			}
		}
	}

	return errors.Join(problems...)
}

// liveEventTypes lists the facts a project can actually listen for.
//
// The core's, plus those of the plugins active for this project: a fact whose plugin is
// switched off will never be produced, and accepting it would give a reaction that never
// leaves with nobody knowing why.
func (h *AutomationsHandler) liveEventTypes(ctx context.Context, projectID int64) ([]string, error) {
	settings, err := h.store.PluginSettingsAll(ctx)
	if err != nil {
		return nil, err
	}
	own, err := h.store.ProjectPluginSettings(ctx, projectID)
	if err != nil {
		return nil, err
	}

	types := make([]string, 0, 12)
	for _, choice := range coreEventChoices() {
		types = append(types, choice.Type)
	}
	for _, descriptor := range plugin.Registry() {
		if !settings[descriptor.ID].Enabled {
			continue
		}
		if entry, declared := own[descriptor.ID]; declared && descriptor.PerProject && !entry.Enabled {
			continue
		}
		for _, spec := range descriptor.Events {
			types = append(types, descriptor.EventType(spec))
		}
	}
	return types, nil
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

func (h *AutomationsHandler) audit(r *http.Request, name, target string, payload map[string]any) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		return
	}
	if err := h.store.Audit(r.Context(), identity.Subject, name, target, payload); err != nil {
		h.logger.Error("journal d'audit", "action", name, "target", target, "error", err)
	}
}
