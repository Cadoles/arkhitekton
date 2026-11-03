package configure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/action"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/configfile"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/installed"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/reaction"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/release"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

/*
 * Applying a configuration: what the files declare is imposed in full, what they do not declare
 * is left alone, unless the prune is asked for.
 *
 * The difference is computed first, as a list of operations a dry run prints. Each operation then
 * goes through the service the screen uses, so that a file cannot write what the screen would
 * refuse. Everything that can be checked without writing is checked before the first write; a
 * failure in the middle names its operation, and applying again converges.
 */

// ApplyStore is what applying needs from the database beyond what the services and the export
// read.
type ApplyStore interface {
	ExportStore
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
}

// Op is one write, described for a dry run.
type Op struct {
	// Description reads `+ reaction portal/Nightly`, `~ plugin gitea`, `- schedule portal/Old`.
	Description string
	run         func(ctx context.Context) error
}

// Applier imposes a configuration on the instance.
type Applier struct {
	Services Services
	Store    ApplyStore
	Registry capabilities.Source
	Catalog  catalog.Source
	// Actor signs the writes in the audit log.
	Actor string
	// Getenv and ReadFile resolve the sensitive settings' references. Nil: the process's.
	Getenv   func(string) (string, bool)
	ReadFile func(string) ([]byte, error)
}

// Plan computes the operations that make the instance match the files. It writes nothing.
//
// The returned warnings name what a value left unchanged because its reference resolves to
// nothing, and what the prune does not touch.
func (a Applier) Plan(ctx context.Context, desired configfile.Set, prune bool) ([]Op, []string, error) {
	checks, err := a.checker()
	if err != nil {
		return nil, nil, err
	}
	current, err := Exporter{Store: a.Store, Registry: a.Registry}.Export(ctx)
	if err != nil {
		return nil, nil, err
	}

	p := &planner{applier: a, prune: prune, checks: checks}
	if desired.Instance != nil {
		p.instance(*desired.Instance, current.Set.Instance)
	}
	for _, project := range desired.Projects {
		existing, _ := current.Set.Project(project.Name)
		p.project(ctx, project, existing)
	}
	if prune {
		for _, project := range current.Set.Projects {
			if _, declared := desired.Project(project.Name); !declared {
				p.warn("project %s is not declared: the prune never removes a project", project.Name)
			}
		}
	}
	return p.ops, p.warnings, errors.Join(p.problems...)
}

// Execute runs the operations in order and stops at the first that fails.
func (a Applier) Execute(ctx context.Context, ops []Op) error {
	for _, op := range ops {
		if err := op.run(ctx); err != nil {
			return fmt.Errorf("%s: %w", op.Description, err)
		}
	}
	return nil
}

type planner struct {
	applier  Applier
	checks   checker
	prune    bool
	ops      []Op
	warnings []string
	problems []error
}

func (p *planner) add(description string, run func(ctx context.Context) error) {
	p.ops = append(p.ops, Op{Description: description, run: run})
}

// check records the problems of what an operation would write. Only what is written is checked:
// an entity the files leave as it is stays, even if it names something the instance lost since.
func (p *planner) check(problems ...error) {
	for _, problem := range problems {
		if problem != nil {
			p.problems = append(p.problems, problem)
		}
	}
}

func (p *planner) warn(format string, args ...any) {
	p.warnings = append(p.warnings, fmt.Sprintf(format, args...))
}

func (p *planner) fail(format string, args ...any) {
	p.problems = append(p.problems, fmt.Errorf(format, args...))
}

/*
 * The instance.
 */

func (p *planner) instance(desired configfile.Instance, current *configfile.Instance) {
	if current == nil {
		current = &configfile.Instance{}
	}
	actor := p.applier.Actor
	services := p.applier.Services

	for _, id := range sortedKeys(desired.Plugins) {
		entry := desired.Plugins[id]
		if entry.Install != nil {
			// Installed from the declared repository, the plugin's settings can be checked: whether it
			// follows or is pinned changes nothing of them.
			wanted, _ := installed.ParseSource(entry.Install.Reference)
			active := current.Plugins[id].Install
			var running installed.Source
			if active != nil {
				running, _ = installed.ParseSource(active.Reference)
			}
			if active == nil || running.Repository != wanted.Repository {
				// Installed by the Installer before this plan is computed; reaching here is a dry run.
				p.warn("plugin %s: its settings are checked once %s is installed", id, entry.Install.Reference)
				continue
			}
		}
		// The export leaves out a plugin as the dashboard seeds it, switched on and empty: absent
		// from the current configuration means on.
		existing := current.Plugins[id]
		input, changed, ok := p.pluginInput(id, plugin.ScopeGlobal, "instance", entry.Enabled, entry.Config,
			existing.Config, configfile.IsOn(existing.Enabled) != configfile.IsOn(entry.Enabled))
		if !ok || !changed {
			continue
		}
		p.add("~ plugin "+id, func(ctx context.Context) error {
			_, _, err := services.Plugins.Set(ctx, actor, id, input)
			return err
		})
	}

	for _, ref := range sortedKeys(desired.CapabilityBindings) {
		binding := desired.CapabilityBindings[ref]
		if existing, recorded := current.CapabilityBindings[ref]; recorded && sameJSON(existing, binding) {
			continue
		}
		p.check(p.checks.binding(ref))
		p.add("~ binding "+ref, func(ctx context.Context) error {
			parsed, err := services.Bindings.Ref(ref)
			if err != nil {
				return err
			}
			_, err = services.Bindings.Set(ctx, actor, parsed, binding)
			return err
		})
	}
	for _, kind := range sortedKeys(desired.QueueLimits) {
		max := desired.QueueLimits[kind]
		if existing, recorded := current.QueueLimits[kind]; recorded && existing == max {
			continue
		}
		p.add(fmt.Sprintf("~ queue limit %s: %d", kind, max), func(ctx context.Context) error {
			return services.QueueLimits.Set(ctx, actor, kind, max)
		})
	}

	if !p.prune {
		return
	}
	for _, ref := range sortedKeys(current.CapabilityBindings) {
		if _, declared := desired.CapabilityBindings[ref]; !declared {
			p.add("- binding "+ref, func(ctx context.Context) error {
				parsed, err := services.Bindings.Ref(ref)
				if err != nil {
					return err
				}
				return services.Bindings.Delete(ctx, actor, parsed)
			})
		}
	}
	for _, kind := range sortedKeys(current.QueueLimits) {
		if _, declared := desired.QueueLimits[kind]; !declared {
			p.add("- queue limit "+kind, func(ctx context.Context) error {
				return services.QueueLimits.Clear(ctx, actor, kind)
			})
		}
	}
	for _, id := range sortedKeys(current.Plugins) {
		if _, declared := desired.Plugins[id]; !declared {
			p.warn("plugin %s is not declared: the prune leaves an instance's plugin settings as they are", id)
		}
	}
}

// pluginInput builds what the plugins' service writes, and says whether it changes anything.
//
// A sensitive field must be a reference. Resolved, it is written (it cannot be compared with the
// sealed value without opening it). Unresolved, the value in place is kept; with none in place, it
// is refused. A sensitive field the file no longer names is cleared.
func (p *planner) pluginInput(
	id string, scope plugin.Scope, where string, enabled *bool,
	desired, current map[string]configfile.Value, switched bool,
) (PluginInput, bool, bool) {
	registry := capabilities.Of(p.applier.Registry)
	descriptor, err := registry.Plugin(id)
	if err != nil {
		p.fail("%s: plugin %s: %v", where, id, err)
		return PluginInput{}, false, false
	}

	input := PluginInput{Enabled: configfile.IsOn(enabled), Config: map[string]string{}}
	changed := switched
	for name, value := range desired {
		if !plugin.IsSecret(descriptor, scope, name) {
			if value.IsRef() {
				p.fail("%s: plugin %s: %s is not sensitive: write its value", where, id, name)
				continue
			}
			input.Config[name] = value.Plain
			if existing, set := current[name]; !set || existing.Plain != value.Plain {
				changed = true
			}
			continue
		}
		if !value.IsRef() {
			p.fail("%s: plugin %s: %s is sensitive: name where it is read from ({env: NAME} or {file: PATH}), never its value",
				where, id, name)
			continue
		}
		resolved, found, err := p.resolve(value)
		switch {
		case err != nil:
			p.fail("%s: plugin %s: %s: %v", where, id, name, err)
		case found:
			input.Config[name] = resolved
			changed = true
		default:
			if _, set := current[name]; !set {
				p.fail("%s: plugin %s: %s: %s is not set and no value is recorded", where, id, name, describeRef(value))
				continue
			}
			p.warn("%s: plugin %s: %s keeps its recorded value (%s is not set)", where, id, name, describeRef(value))
		}
	}
	for name := range current {
		if _, declared := desired[name]; declared {
			continue
		}
		if plugin.IsSecret(descriptor, scope, name) {
			input.ClearedSecrets = append(input.ClearedSecrets, name)
		}
		changed = true
	}
	slices.Sort(input.ClearedSecrets)
	return input, changed, true
}

func (p *planner) resolve(value configfile.Value) (string, bool, error) {
	if value.Env != "" {
		getenv := p.applier.Getenv
		if getenv == nil {
			getenv = os.LookupEnv
		}
		resolved, found := getenv(value.Env)
		return resolved, found && resolved != "", nil
	}
	read := p.applier.ReadFile
	if read == nil {
		read = os.ReadFile
	}
	raw, err := read(value.File)
	if err != nil {
		return "", false, err
	}
	return strings.TrimRight(string(raw), "\r\n"), true, nil
}

func describeRef(value configfile.Value) string {
	if value.Env != "" {
		return "the variable " + value.Env
	}
	return "the file " + value.File
}

/*
 * A project.
 */

func (p *planner) project(ctx context.Context, desired configfile.Project, current configfile.Project) {
	actor := p.applier.Actor
	services := p.applier.Services
	slug := desired.Name
	exists := current.Name != ""
	find := func(ctx context.Context) (store.Project, error) { return p.applier.Store.ProjectBySlug(ctx, slug) }

	input := projectInput(desired)
	normalised, err := services.Projects.Normalise(ctx, input)
	if err != nil {
		p.fail("project %s: %v", slug, err)
		return
	}
	switch {
	case !exists:
		p.add("+ project "+slug, func(ctx context.Context) error {
			_, err := services.Projects.Create(ctx, actor, input)
			return err
		})
	default:
		was, _ := services.Projects.Normalise(ctx, projectInput(current))
		if fields := changedFields(was, normalised); len(fields) > 0 {
			// Omitted, the cycle is the default one: the store keeps the recorded cycle when it is
			// given none, which would leave the difference in place for ever.
			if len(input.ReleaseKinds) == 0 {
				input.ReleaseKinds = release.DefaultCycle()
			}
			p.add("~ project "+slug+": "+strings.Join(fields, ", "), func(ctx context.Context) error {
				project, err := find(ctx)
				if err != nil {
					return err
				}
				return services.Projects.Update(ctx, actor, project.ID, input)
			})
		}
	}

	for _, id := range sortedKeys(desired.Plugins) {
		entry := desired.Plugins[id]
		existing, recorded := current.Plugins[id]
		where := "project " + slug
		input, changed, ok := p.pluginInput(id, plugin.ScopeProject, where, entry.Enabled, entry.Config,
			existing.Config, !recorded || configfile.IsOn(existing.Enabled) != configfile.IsOn(entry.Enabled))
		if !ok || !changed {
			continue
		}
		p.add("~ plugin "+slug+"/"+id, func(ctx context.Context) error {
			project, err := find(ctx)
			if err != nil {
				return err
			}
			_, _, err = services.Plugins.SetProject(ctx, actor, project, id, input)
			return err
		})
	}

	for _, kind := range sortedKeys(desired.Pipelines) {
		enabled := desired.Pipelines[kind]
		if existing, declared := current.Pipelines[kind]; declared && existing == enabled {
			continue
		}
		p.check(p.checks.kind("project "+slug+": pipeline", kind))
		p.add(fmt.Sprintf("~ pipeline %s/%s: %s", slug, kind, onOff(enabled)), func(ctx context.Context) error {
			project, err := find(ctx)
			if err != nil {
				return err
			}
			_, err = services.ProjectPipelines.Set(ctx, actor, project, kind, enabled)
			return err
		})
	}

	for _, status := range sortedKeys(desired.TrackerStatuses) {
		bucket := desired.TrackerStatuses[status]
		if current.TrackerStatuses[status] == bucket {
			continue
		}
		p.add(fmt.Sprintf("~ tracker status %s/%s: %s", slug, status, bucket), func(ctx context.Context) error {
			project, err := find(ctx)
			if err != nil {
				return err
			}
			_, err = services.StatusMappings.Set(ctx, actor, project, status, bucket)
			return err
		})
	}

	if desired.ReleaseAutomation != nil && (current.ReleaseAutomation == nil ||
		!samePlan(*current.ReleaseAutomation, *desired.ReleaseAutomation)) {
		automation := *desired.ReleaseAutomation
		p.check(p.checks.plan("project "+slug+": release automation", automation))
		p.add("~ release automation "+slug, func(ctx context.Context) error {
			project, err := find(ctx)
			if err != nil {
				return err
			}
			return services.Automations.SaveReleaseAutomation(ctx, actor, project, automation)
		})
	}

	p.reactions(ctx, slug, find, desired.Reactions, exists)
	p.schedules(ctx, slug, find, desired.Schedules, exists)

	if !p.prune || !exists {
		return
	}
	for _, id := range sortedKeys(current.Plugins) {
		if _, declared := desired.Plugins[id]; !declared {
			p.add("- plugin "+slug+"/"+id+" (back to the instance's settings)", func(ctx context.Context) error {
				project, err := find(ctx)
				if err != nil {
					return err
				}
				_, err = services.Plugins.ClearProject(ctx, actor, project, id)
				return err
			})
		}
	}
	for _, kind := range sortedKeys(current.Pipelines) {
		if _, declared := desired.Pipelines[kind]; !declared {
			p.add("- pipeline "+slug+"/"+kind+" (back to the default)", func(ctx context.Context) error {
				project, err := find(ctx)
				if err != nil {
					return err
				}
				return services.ProjectPipelines.Clear(ctx, actor, project, kind)
			})
		}
	}
	for _, status := range sortedKeys(current.TrackerStatuses) {
		if _, declared := desired.TrackerStatuses[status]; !declared {
			p.add("- tracker status "+slug+"/"+status, func(ctx context.Context) error {
				project, err := find(ctx)
				if err != nil {
					return err
				}
				return services.StatusMappings.Clear(ctx, actor, project, status)
			})
		}
	}
}

func (p *planner) reactions(ctx context.Context, slug string, find func(context.Context) (store.Project, error), desired []configfile.Reaction, exists bool) {
	actor := p.applier.Actor
	automations := p.applier.Services.Automations
	recorded := map[string]reaction.Reaction{}
	if exists {
		p.load(ctx, func(ctx context.Context) error {
			project, err := find(ctx)
			if err != nil {
				return err
			}
			entries, err := p.applier.Store.ListReactions(ctx, project.ID)
			for _, entry := range entries {
				recorded[configfile.NameKey(entry.Name)] = entry
			}
			return err
		})
	}

	declared := map[string]bool{}
	for _, entry := range desired {
		key := configfile.NameKey(entry.Name)
		declared[key] = true
		wanted := reaction.Reaction{
			Name: strings.TrimSpace(entry.Name), EventType: strings.TrimSpace(entry.Event),
			Enabled: configfile.IsOn(entry.Enabled), Match: nonEmpty(entry.Match), Plan: entry.Plan,
		}
		existing, found := recorded[key]
		if found {
			wanted.ID = existing.ID
			existing.ProjectID, existing.Match = 0, nonEmpty(existing.Match)
			if reflect.DeepEqual(comparableReaction(existing), comparableReaction(wanted)) {
				continue
			}
		}
		sign := "+"
		if found {
			sign = "~"
		}
		where := fmt.Sprintf("project %s: reaction %q", slug, wanted.Name)
		p.check(p.checks.event(where, wanted.EventType), p.checks.plan(where, wanted.Plan))
		p.add(sign+" reaction "+slug+"/"+wanted.Name, func(ctx context.Context) error {
			project, err := find(ctx)
			if err != nil {
				return err
			}
			_, err = automations.SaveReaction(ctx, actor, project, wanted)
			return err
		})
	}
	if !p.prune {
		return
	}
	for _, key := range sortedKeys(recorded) {
		if declared[key] {
			continue
		}
		entry := recorded[key]
		p.add("- reaction "+slug+"/"+entry.Name+" (its history keeps naming it by its id)", func(ctx context.Context) error {
			project, err := find(ctx)
			if err != nil {
				return err
			}
			return automations.DeleteReaction(ctx, actor, project, entry.ID)
		})
	}
}

func (p *planner) schedules(ctx context.Context, slug string, find func(context.Context) (store.Project, error), desired []configfile.Schedule, exists bool) {
	actor := p.applier.Actor
	automations := p.applier.Services.Automations
	recorded := map[string]store.RecordedSchedule{}
	if exists {
		p.load(ctx, func(ctx context.Context) error {
			project, err := find(ctx)
			if err != nil {
				return err
			}
			entries, err := p.applier.Store.ListSchedules(ctx, project.ID)
			for _, entry := range entries {
				recorded[configfile.NameKey(entry.Name)] = entry
			}
			return err
		})
	}

	declared := map[string]bool{}
	for _, entry := range desired {
		key := configfile.NameKey(entry.Name)
		declared[key] = true
		wanted := entry.Schedule(0)
		existing, found := recorded[key]
		if found {
			wanted.ID = existing.ID
			was := existing.Schedule
			was.ProjectID = 0
			if was.Name == wanted.Name && was.Cron == wanted.Cron && was.Timezone == wanted.Timezone &&
				was.Ref == wanted.Ref && was.Enabled == wanted.Enabled && samePlan(was.Plan, wanted.Plan) {
				continue
			}
		}
		sign := "+"
		if found {
			sign = "~"
		}
		p.check(p.checks.plan(fmt.Sprintf("project %s: schedule %q", slug, wanted.Name), wanted.Plan))
		p.add(sign+" schedule "+slug+"/"+wanted.Name, func(ctx context.Context) error {
			project, err := find(ctx)
			if err != nil {
				return err
			}
			_, _, err = automations.SaveSchedule(ctx, actor, project, wanted)
			return err
		})
	}
	if !p.prune {
		return
	}
	for _, key := range sortedKeys(recorded) {
		if declared[key] {
			continue
		}
		entry := recorded[key]
		p.add("- schedule "+slug+"/"+entry.Name+" (its history keeps naming it by its id)", func(ctx context.Context) error {
			project, err := find(ctx)
			if err != nil {
				return err
			}
			return automations.DeleteSchedule(ctx, actor, project, entry.ID)
		})
	}
}

// load reads what the plan needs from the database while it is computed. Plan runs before any
// write, so a read here sees the instance as it is.
func (p *planner) load(ctx context.Context, read func(ctx context.Context) error) {
	if err := read(ctx); err != nil {
		p.problems = append(p.problems, err)
	}
}

func comparableReaction(entry reaction.Reaction) any {
	plan, _ := json.Marshal(entry.Plan)
	return struct {
		Name, EventType string
		Enabled         bool
		Match           map[string]string
		Plan            string
	}{entry.Name, entry.EventType, entry.Enabled, entry.Match, string(plan)}
}

func projectInput(project configfile.Project) ProjectInput {
	targets := make([]TargetInput, 0, len(project.Targets))
	for _, target := range project.Targets {
		targets = append(targets, TargetInput{
			Stage: target.Stage, Image: target.Image, Dockerfile: target.Dockerfile,
			Promote: configfile.IsOn(target.Promote),
		})
	}
	return ProjectInput{
		Repository: project.Repository, Label: project.Label, ForgeURL: project.ForgeURL,
		Dockerfile: project.Dockerfile, BuildContext: project.BuildContext, Slug: project.Name,
		ImageRegistry: project.ImageRegistry, RegistrySecret: project.RegistrySecret,
		VersionScheme: project.VersionScheme, Stack: project.Stack, TrackerProject: project.TrackerProject,
		MultiStage: project.MultiStage, Targets: targets, ReleaseKinds: project.ReleaseKinds,
		VersionFile: project.VersionFile,
	}
}

// changedFields names the fields that differ between two normalised descriptions. An empty cycle
// stands for the default one, as it does when a project is written.
func changedFields(was, wanted ProjectInput) []string {
	if len(was.ReleaseKinds) == 0 {
		was.ReleaseKinds = release.DefaultCycle()
	}
	if len(wanted.ReleaseKinds) == 0 {
		wanted.ReleaseKinds = release.DefaultCycle()
	}
	if wanted.VersionScheme == "" {
		wanted.VersionScheme = "semver"
	}
	if was.VersionScheme == "" {
		was.VersionScheme = "semver"
	}
	var fields []string
	before, after := reflect.ValueOf(was), reflect.ValueOf(wanted)
	for index := range before.NumField() {
		if !reflect.DeepEqual(before.Field(index).Interface(), after.Field(index).Interface()) {
			name := before.Type().Field(index).Name
			fields = append(fields, strings.ToLower(name[:1])+name[1:])
		}
	}
	return fields
}

func samePlan(a, b plan.Plan) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func sameJSON(a, b []byte) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

func onOff(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}

/*
 * What can be checked before writing anything.
 */

// checker refuses what names nothing in this instance: a kind missing from the catalogue, an
// unknown action or fact, a binding for a capability no plugin declares. Whether a plugin is
// switched on for a project depends on the order of the writes, and is checked by the services when
// they write.
type checker struct {
	registry *capabilities.Registry
	catalog  *catalog.Catalog
	actions  action.Catalog
	events   map[string]bool
}

func (a Applier) checker() (checker, error) {
	registry := capabilities.Of(a.Registry)
	if registry == nil {
		return checker{}, errors.New("no plugin registry: nothing can be checked")
	}
	events := map[string]bool{}
	for _, core := range CoreEvents() {
		events[core.Type] = true
	}
	for _, descriptor := range registry.Plugins() {
		for _, spec := range descriptor.Events {
			events[descriptor.EventType(spec)] = true
		}
	}
	return checker{
		registry: registry, catalog: catalog.Of(a.Catalog),
		actions: action.NewCatalog(registry.Plugins()), events: events,
	}, nil
}

func (c checker) kind(where, kind string) error {
	if c.catalog == nil {
		return fmt.Errorf("%s: kind %q cannot be checked: no catalogue is loaded", where, kind)
	}
	if _, found := c.catalog.Find(kind); !found {
		return fmt.Errorf("%s: kind %q is missing from the catalogue", where, kind)
	}
	return nil
}

func (c checker) plan(where string, entry plan.Plan) error {
	var problems []error
	pipelines, refs := plan.Refs(entry)
	for _, ref := range pipelines {
		problems = append(problems, c.kind(where, ref))
	}
	for _, ref := range refs {
		if _, err := c.actions.Get(ref); err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", where, err))
		}
	}
	return errors.Join(problems...)
}

func (c checker) event(where, event string) error {
	if !c.events[strings.TrimSpace(event)] {
		return fmt.Errorf("%s: no plugin of this instance produces %q", where, event)
	}
	return nil
}

func (c checker) binding(ref string) error {
	parsed, err := capability.ParseRef(ref)
	if err != nil {
		return fmt.Errorf("instance: binding %q: %w", ref, err)
	}
	if _, _, found := c.registry.Pipeline(parsed); !found {
		return fmt.Errorf("instance: binding %q: no plugin declares this capability", ref)
	}
	return nil
}
