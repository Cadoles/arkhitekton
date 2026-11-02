package configure

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/action"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/reaction"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/schedule"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// AutomationStore is what a project's plans need from the database.
type AutomationStore interface {
	SaveReaction(ctx context.Context, entry reaction.Reaction, actor string) (int64, error)
	DeleteReaction(ctx context.Context, projectID, id int64) error
	SaveReleaseAutomation(ctx context.Context, entry store.ReleaseAutomation, actor string) error
	PluginSettingsAll(ctx context.Context) (map[string]store.PluginSettings, error)
	ProjectPluginSettings(ctx context.Context, projectID int64) (map[string]store.PluginSettings, error)
	Auditor
}

// ScheduleStore is what the schedules need from the database.
type ScheduleStore interface {
	SaveSchedule(ctx context.Context, entry schedule.Schedule, next *time.Time, actor string) (int64, error)
	DeleteSchedule(ctx context.Context, projectID, id int64) error
}

// Automations writes a project's plans: its release automation, its reactions, its schedules.
//
// `internal/plan`, `internal/reaction` and `internal/schedule` carry the shapes; this service
// carries the **references**: that a fact type is produced by an active plugin, that a step names
// a kind of the catalogue or an action of an enabled plugin, that a stage's role is held by what
// it runs.
type Automations struct {
	Store AutomationStore
	// Schedules is nil where the schedules are not served.
	Schedules ScheduleStore
	Catalog   catalog.Source
	Registry  capabilities.Source
	// Now gives the instant a schedule's next firing is computed from. Nil: the clock.
	Now    func() time.Time
	Logger *slog.Logger
}

// CoreEvent is a fact the core produces, with its matchable fields.
type CoreEvent struct {
	Type   string
	Label  string
	Fields []string
}

// pullRequestFields are what a pull request's facts carry. `revision`, `tag` and `registry` are
// what a plan's steps take as a release's: a plan opened by such a fact builds and tests the head.
var pullRequestFields = []string{
	"number", "title", "author", "source_branch", "target_branch", "url",
	"revision", "tag", "registry", "pull_request_id",
}

// CoreEvents lists the facts the core produces, whatever plugins are enabled.
func CoreEvents() []CoreEvent {
	return []CoreEvent{
		{Type: string(event.VersionCreated), Label: "Version declared in the tracker",
			Fields: []string{"version_id", "name", "status", "tool", "external_id", "due_date"}},
		{Type: string(event.ReleaseCreated), Label: "Release declared",
			Fields: []string{"release_id", "tag", "kind", "git_ref", "version"}},
		{Type: string(event.ReleaseReleased), Label: "Release validated",
			Fields: []string{"release_id", "tag", "released_by"}},
		{Type: string(event.PullRequestOpened), Label: "Pull request opened", Fields: pullRequestFields},
		{Type: string(event.PullRequestSynchronized), Label: "Pull request pushed to", Fields: pullRequestFields},
		{Type: string(event.PullRequestClosed), Label: "Pull request closed without merging", Fields: pullRequestFields},
		{Type: string(event.PullRequestMerged), Label: "Pull request merged", Fields: pullRequestFields},
		{Type: string(event.PullRequestReopened), Label: "Pull request reopened", Fields: pullRequestFields},
	}
}

// SaveReleaseAutomation replaces a project's automation.
//
// An empty plan is accepted, deliberately: it is how to say "this project no longer builds".
// plan.Validate would refuse it (a reaction with no stage reacts to nothing), so it is only
// checked when it carries something.
func (s Automations) SaveReleaseAutomation(ctx context.Context, actor string, project store.Project, entry plan.Plan) error {
	logger := loggerOr(s.Logger)
	if len(entry.Stages) > 0 {
		if err := entry.Validate(); err != nil {
			return invalid("Invalid automation", err)
		}
		if err := s.CheckPlanRefs(ctx, entry); err != nil {
			return invalid("Invalid automation", err)
		}
		if err := s.checkRoles(entry); err != nil {
			return invalid("Invalid automation", err)
		}
	}

	err := s.Store.SaveReleaseAutomation(ctx, store.ReleaseAutomation{ProjectID: project.ID, Plan: entry}, actor)
	if err != nil {
		return failure(logger, "Cannot save", err, "project", project.Slug)
	}

	// The log carries the count of stages and steps, not the plan: it carries templates, and an
	// audit log is read with fewer precautions than a database. The count is enough to see that an
	// automation was emptied.
	audit(ctx, logger, s.Store, actor, "automation.save", project.Slug, map[string]any{
		"stages": len(entry.Stages), "steps": stepCount(entry),
	})
	return nil
}

func stepCount(p plan.Plan) int {
	total := 0
	for _, stage := range p.Stages {
		total += len(stage.Gestes)
	}
	return total
}

// SaveReaction creates the reaction when its id is zero, replaces it otherwise.
func (s Automations) SaveReaction(ctx context.Context, actor string, project store.Project, entry reaction.Reaction) (int64, error) {
	logger := loggerOr(s.Logger)
	entry.ProjectID = project.ID
	entry.Name = strings.TrimSpace(entry.Name)
	entry.EventType = strings.TrimSpace(entry.EventType)

	if err := entry.Validate(); err != nil {
		return 0, invalid("Invalid reaction", err)
	}
	if err := s.checkReactionRefs(ctx, entry); err != nil {
		return 0, invalid("Invalid reaction", err)
	}

	saved, err := s.Store.SaveReaction(ctx, entry, actor)
	switch {
	case errors.Is(err, store.ErrReactionExists):
		return 0, refuse(http.StatusConflict, "Name already taken", "a reaction of this project already carries this name")
	case errors.Is(err, store.ErrReactionNotFound):
		return 0, refuse(http.StatusNotFound, "Reaction not found", "")
	case err != nil:
		return 0, failure(logger, "Cannot save", err, "project", project.Slug, "name", entry.Name)
	}

	// The log carries the fact listened for and whether it is enabled. Not the plan: it carries
	// templates, and an audit log is read with fewer precautions than a database.
	audit(ctx, logger, s.Store, actor, "reaction.save", project.Slug+"/"+entry.Name, map[string]any{
		"event_type": entry.EventType, "enabled": entry.Enabled,
	})
	return saved, nil
}

// DeleteReaction removes a reaction.
func (s Automations) DeleteReaction(ctx context.Context, actor string, project store.Project, id int64) error {
	logger := loggerOr(s.Logger)
	if err := s.Store.DeleteReaction(ctx, project.ID, id); err != nil {
		if errors.Is(err, store.ErrReactionNotFound) {
			return refuse(http.StatusNotFound, "Reaction not found", "")
		}
		return failure(logger, "Cannot delete", err, "project", project.Slug, "reaction", id)
	}
	audit(ctx, logger, s.Store, actor, "reaction.delete", project.Slug+"/"+strconv.FormatInt(id, 10), nil)
	return nil
}

// SaveSchedule creates the schedule when its id is zero, replaces it otherwise, and returns its next
// firing. The next firing is computed from now: switching a schedule back on does not fire the
// ticks it slept through.
func (s Automations) SaveSchedule(ctx context.Context, actor string, project store.Project, entry schedule.Schedule) (int64, *time.Time, error) {
	logger := loggerOr(s.Logger)
	if s.Schedules == nil {
		return 0, nil, refuse(http.StatusNotFound, "Schedules unavailable", "")
	}
	entry.ProjectID = project.ID
	entry.Name = strings.TrimSpace(entry.Name)
	entry.Cron = strings.TrimSpace(entry.Cron)
	entry.Timezone = strings.TrimSpace(entry.Timezone)
	entry.Ref = strings.TrimSpace(entry.Ref)

	if err := entry.Validate(); err != nil {
		return 0, nil, invalid("Invalid schedule", err)
	}
	if err := s.checkScheduleRefs(ctx, entry); err != nil {
		return 0, nil, invalid("Invalid schedule", err)
	}

	var next *time.Time
	if entry.Enabled {
		at, err := entry.Next(s.now())
		if err != nil {
			return 0, nil, invalid("Invalid schedule", err)
		}
		next = &at
	}

	saved, err := s.Schedules.SaveSchedule(ctx, entry, next, actor)
	switch {
	case errors.Is(err, store.ErrScheduleExists):
		return 0, nil, refuse(http.StatusConflict, "Name already taken", "a schedule of this project already carries this name")
	case errors.Is(err, store.ErrScheduleNotFound):
		return 0, nil, refuse(http.StatusNotFound, "Schedule not found", "")
	case err != nil:
		return 0, nil, failure(logger, "Cannot save", err, "project", project.Slug, "name", entry.Name)
	}

	audit(ctx, logger, s.Store, actor, "schedule.save", project.Slug+"/"+entry.Name, map[string]any{
		"cron": entry.Cron, "timezone": entry.Timezone, "ref": entry.Ref, "enabled": entry.Enabled,
	})
	return saved, next, nil
}

// DeleteSchedule removes a schedule.
func (s Automations) DeleteSchedule(ctx context.Context, actor string, project store.Project, id int64) error {
	logger := loggerOr(s.Logger)
	if s.Schedules == nil {
		return refuse(http.StatusNotFound, "Schedules unavailable", "")
	}
	if err := s.Schedules.DeleteSchedule(ctx, project.ID, id); err != nil {
		if errors.Is(err, store.ErrScheduleNotFound) {
			return refuse(http.StatusNotFound, "Schedule not found", "")
		}
		return failure(logger, "Cannot delete", err, "project", project.Slug, "schedule", id)
	}
	audit(ctx, logger, s.Store, actor, "schedule.delete", project.Slug+"/"+strconv.FormatInt(id, 10), nil)
	return nil
}

func (s Automations) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// checkReactionRefs refuses a reaction one of whose references names nothing.
func (s Automations) checkReactionRefs(ctx context.Context, entry reaction.Reaction) error {
	var problems []error

	live, err := s.LiveEventTypes(ctx, entry.ProjectID)
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
	problems = append(problems, noRoles("reaction", entry.Plan))
	problems = append(problems, s.CheckPlanRefs(ctx, entry.Plan))
	return errors.Join(problems...)
}

func (s Automations) checkScheduleRefs(ctx context.Context, entry schedule.Schedule) error {
	return errors.Join(noRoles("schedule", entry.Plan), s.CheckPlanRefs(ctx, entry.Plan))
}

func noRoles(owner string, entry plan.Plan) error {
	var problems []error
	for index, stage := range entry.Stages {
		if stage.Role != "" {
			problems = append(problems, fmt.Errorf(
				"stage %d of a %s carries the role %q: a role only advances "+
					"a release's automation", index+1, owner, stage.Role))
		}
	}
	return errors.Join(problems...)
}

// CheckPlanRefs refuses a plan one of whose steps names nothing executable.
//
// Shared by the reactions, the schedules and a release's automation: they carry the same
// structure, and three checks would diverge at the first addition.
func (s Automations) CheckPlanRefs(ctx context.Context, entry plan.Plan) error {
	var problems []error

	pipelines, actions := plan.Refs(entry)
	for _, ref := range pipelines {
		if s.Catalog == nil {
			problems = append(problems, fmt.Errorf("kind %q cannot be checked: no catalogue is loaded", ref))
			continue
		}
		if _, found := catalog.Of(s.Catalog).Find(ref); !found {
			problems = append(problems, fmt.Errorf(
				"kind %q is missing from the catalogue (declared: %s)",
				ref, strings.Join(catalog.Of(s.Catalog).Kinds(), ", ")))
		}
	}

	settings, err := s.Store.PluginSettingsAll(ctx)
	if err != nil {
		return err
	}
	known := action.NewCatalog(capabilities.Of(s.Registry).Plugins())
	for _, ref := range actions {
		descriptor, err := known.Get(ref)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		// A step whose plugin is switched off is refused when saved, like a fact no active plugin
		// produces. The symmetry is not aesthetic: in both cases the reaction would be declared,
		// visible, and without effect, which is what must not be suggested.
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
			descriptor, err := known.Get(step.Ref)
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

// LiveEventTypes lists the facts a project can actually listen for.
//
// The core's, plus those of the plugins active for this project: a fact whose plugin is switched
// off will never be produced, and accepting it would give a reaction that never leaves with nobody
// knowing why.
func (s Automations) LiveEventTypes(ctx context.Context, projectID int64) ([]string, error) {
	settings, err := s.Store.PluginSettingsAll(ctx)
	if err != nil {
		return nil, err
	}
	own, err := s.Store.ProjectPluginSettings(ctx, projectID)
	if err != nil {
		return nil, err
	}

	types := make([]string, 0, 12)
	for _, core := range CoreEvents() {
		types = append(types, core.Type)
	}
	for _, descriptor := range capabilities.Of(s.Registry).Plugins() {
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

// roleProduct says what a step must produce to hold a role. A table and not a switch: it reads at
// a glance, and the error message uses it to name what it expected.
var roleProduct = map[plan.Role]catalog.Product{
	plan.RoleBuild: catalog.ProductImage,
	plan.RoleTest:  catalog.ProductResults,
}

// checkRoles refuses a stage whose steps do not hold its role.
//
// Without that check, a release's life cycle would change by accident. Two cases, and the second
// is the more insidious:
//
//   - a test step in a `build` stage: the release would move to verification as soon as the tests
//     concluded, that is, after them;
//   - an action in a stage with a role: a failed webhook would fail the release, although it has
//     nothing to say about its quality.
func (s Automations) checkRoles(entry plan.Plan) error {
	var problems []error

	for index, stage := range entry.Stages {
		expected, carries := roleProduct[stage.Role]
		if !carries {
			// A stage with no role touches nothing: it may carry what it likes. It is what allows
			// warning a team without putting the release's state at stake.
			continue
		}

		for _, step := range stage.Gestes {
			if step.Kind != plan.GestePipeline {
				problems = append(problems, fmt.Errorf(
					"stage %d carries the role %q and the step %q is not a pipeline: "+
						"its failure would fail the release without saying anything about its quality",
					index+1, stage.Role, step.Ref))
				continue
			}
			if s.Catalog == nil {
				problems = append(problems, fmt.Errorf(
					"the role of stage %d cannot be checked: no catalogue is loaded", index+1))
				continue
			}
			pipeline, found := catalog.Of(s.Catalog).Find(step.Ref)
			if !found {
				// Already reported by the reference check. Repeating it here would give two messages
				// for one mistake.
				continue
			}
			if !pipeline.Yields(expected) {
				problems = append(problems, fmt.Errorf(
					"stage %d carries the role %q and the pipeline %q does not produce %q "+
						"(il produit: %s)",
					index+1, stage.Role, step.Ref, expected, productsOf(pipeline)))
			}
		}
	}

	return errors.Join(problems...)
}

// productsOf names what a pipeline declares it produces, for a message.
func productsOf(pipeline catalog.Pipeline) string {
	if len(pipeline.Produces) == 0 {
		return "nothing declared"
	}
	names := make([]string, 0, len(pipeline.Produces))
	for _, product := range pipeline.Produces {
		names = append(names, string(product))
	}
	return strings.Join(names, ", ")
}
