package configure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/configfile"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/reaction"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/release"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// ExportStore is what reading a configuration back needs from the database.
type ExportStore interface {
	PluginSettingsAll(ctx context.Context) (map[string]store.PluginSettings, error)
	ProjectPluginSettings(ctx context.Context, projectID int64) (map[string]store.PluginSettings, error)
	PluginInstallations(ctx context.Context) ([]store.PluginInstallation, error)
	PluginSources(ctx context.Context) ([]store.PluginSource, error)
	AcceptedSources(ctx context.Context) ([]string, error)
	QueueLimits(ctx context.Context) (map[string]int, error)
	CapabilityBindings(ctx context.Context) ([]store.CapabilityBinding, error)
	ListProjects(ctx context.Context) ([]store.Project, error)
	ProjectPipelines(ctx context.Context, projectID int64) (map[string]store.ProjectPipeline, error)
	OwnStatusMapping(ctx context.Context, projectID int64) (map[string]string, error)
	ReleaseAutomation(ctx context.Context, projectID int64) (store.ReleaseAutomation, error)
	ListReactions(ctx context.Context, projectID int64) ([]reaction.Reaction, error)
	ListSchedules(ctx context.Context, projectID int64) ([]store.RecordedSchedule, error)
}

// Exported is a configuration read back from the database, with what writing it out leaves to the
// reader.
type Exported struct {
	Set configfile.Set
	// Secrets are the environment variables the sensitive settings were replaced by, sorted.
	Secrets []string
	// Warnings name what was left out, and why.
	Warnings []string
}

// Exporter reads an instance's configuration back as resources.
//
// What it writes is what an administrator decided, never what the dashboard observed: no access
// verdict, no deploy key, no next firing. A sensitive setting comes out as a named reference, never
// as its value, sealed or not.
type Exporter struct {
	Store    ExportStore
	Registry capabilities.Source
}

// Export reads the whole configuration.
func (e Exporter) Export(ctx context.Context) (Exported, error) {
	out := Exported{}
	secrets := map[string]bool{}

	instance, err := e.instance(ctx, &out, secrets)
	if err != nil {
		return Exported{}, err
	}
	out.Set.Instance = instance

	projects, err := e.Store.ListProjects(ctx)
	if err != nil {
		return Exported{}, err
	}
	for _, project := range projects {
		declared, err := e.project(ctx, project, &out, secrets)
		if err != nil {
			return Exported{}, fmt.Errorf("project %s: %w", project.Slug, err)
		}
		out.Set.Projects = append(out.Set.Projects, declared)
	}
	slices.SortFunc(out.Set.Projects, func(a, b configfile.Project) int { return strings.Compare(a.Name, b.Name) })

	for name := range secrets {
		out.Secrets = append(out.Secrets, name)
	}
	slices.Sort(out.Secrets)
	return out, nil
}

func (e Exporter) instance(ctx context.Context, out *Exported, secrets map[string]bool) (*configfile.Instance, error) {
	settings, err := e.Store.PluginSettingsAll(ctx)
	if err != nil {
		return nil, err
	}
	installations, err := e.Store.PluginInstallations(ctx)
	if err != nil {
		return nil, err
	}
	accepted, err := e.Store.AcceptedSources(ctx)
	if err != nil {
		return nil, err
	}
	sources, err := e.Store.PluginSources(ctx)
	if err != nil {
		return nil, err
	}
	following := map[string]string{}
	for _, source := range sources {
		if !source.Pinned {
			following[source.PluginID] = source.Repository
		}
	}
	installed := map[string]store.PluginInstallation{}
	for _, installation := range installations {
		if installation.Active && installation.State == store.InstallationInstalled {
			installed[installation.PluginID] = installation
		}
	}

	instance := &configfile.Instance{}
	ids := sortedKeys(settings)
	for id := range installed {
		if _, recorded := settings[id]; !recorded {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		descriptor, known := e.descriptor(id)
		if !known {
			out.Warnings = append(out.Warnings, fmt.Sprintf(
				"plugin %q has settings but no plugin of this instance carries it: left out", id))
			continue
		}
		entry := settings[id]
		declared := configfile.InstancePlugin{
			Enabled: configfile.On(entry.Enabled),
			Config:  exportConfig(descriptor, plugin.ScopeGlobal, entry.Config, secretName("", id), secrets),
		}
		if installation, ok := installed[id]; ok {
			// A plugin following its repository is written as the repository: applying the file again
			// keeps it following rather than pinning it to today's version.
			reference := installation.Reference
			if repository, follows := following[id]; follows {
				reference = repository
			}
			declared.Install = &configfile.Install{Reference: reference, AcceptSource: slices.Contains(accepted, id)}
		}
		// A plugin left as the dashboard seeds it says nothing: writing it would only make the
		// file longer and tie it to this binary's set of plugins.
		if declared.Install == nil && declared.Enabled == nil && len(declared.Config) == 0 {
			continue
		}
		if instance.Plugins == nil {
			instance.Plugins = map[string]configfile.InstancePlugin{}
		}
		instance.Plugins[id] = declared
	}

	limits, err := e.Store.QueueLimits(ctx)
	if err != nil {
		return nil, err
	}
	if len(limits) > 0 {
		instance.QueueLimits = limits
	}

	bindings, err := e.Store.CapabilityBindings(ctx)
	if err != nil {
		return nil, err
	}
	for _, binding := range bindings {
		if instance.CapabilityBindings == nil {
			instance.CapabilityBindings = map[string]json.RawMessage{}
		}
		instance.CapabilityBindings[binding.Ref] = binding.Binding
	}
	return instance, nil
}

func (e Exporter) project(
	ctx context.Context, project store.Project, out *Exported, secrets map[string]bool,
) (configfile.Project, error) {
	declared := configfile.Project{
		Name: project.Slug, Repository: project.Repository, Label: project.Label,
		ForgeURL: project.ForgeURL, Dockerfile: project.Dockerfile, ImageRegistry: project.ImageRegistry,
		RegistrySecret: project.RegistrySecret, Stack: project.Stack, TrackerProject: project.TrackerProject,
		MultiStage: project.MultiStage,
	}
	// The defaults are left out: written, they would pin this instance's choice where the file
	// means "whatever the dashboard does by default".
	if project.BuildContext != "." {
		declared.BuildContext = project.BuildContext
	}
	if project.VersionScheme != "semver" {
		declared.VersionScheme = project.VersionScheme
	}
	if !reflect.DeepEqual(project.ReleaseKinds, release.DefaultCycle()) {
		declared.ReleaseKinds = project.ReleaseKinds
	}
	for _, target := range project.Targets {
		declared.Targets = append(declared.Targets, configfile.Target{
			Stage: target.Stage, Image: target.Image, Dockerfile: target.Dockerfile,
			Promote: configfile.On(target.Promote == nil || *target.Promote),
		})
	}

	own, err := e.Store.ProjectPluginSettings(ctx, project.ID)
	if err != nil {
		return declared, err
	}
	for _, id := range sortedKeys(own) {
		descriptor, known := e.descriptor(id)
		if !known {
			out.Warnings = append(out.Warnings, fmt.Sprintf(
				"project %s: plugin %q has settings but no plugin of this instance carries it: left out", project.Slug, id))
			continue
		}
		if declared.Plugins == nil {
			declared.Plugins = map[string]configfile.ProjectPlugin{}
		}
		declared.Plugins[id] = configfile.ProjectPlugin{
			Enabled: configfile.On(own[id].Enabled),
			Config:  exportConfig(descriptor, plugin.ScopeProject, own[id].Config, secretName(project.Slug, id), secrets),
		}
	}

	pipelines, err := e.Store.ProjectPipelines(ctx, project.ID)
	if err != nil {
		return declared, err
	}
	for kind, entry := range pipelines {
		if declared.Pipelines == nil {
			declared.Pipelines = map[string]bool{}
		}
		declared.Pipelines[kind] = entry.Enabled
	}

	statuses, err := e.Store.OwnStatusMapping(ctx, project.ID)
	if err != nil {
		return declared, err
	}
	if len(statuses) > 0 {
		declared.TrackerStatuses = statuses
	}

	automation, err := e.Store.ReleaseAutomation(ctx, project.ID)
	switch {
	case errors.Is(err, store.ErrNoAutomation):
	case err != nil:
		return declared, err
	default:
		declared.ReleaseAutomation = &automation.Plan
	}

	reactions, err := e.Store.ListReactions(ctx, project.ID)
	if err != nil {
		return declared, err
	}
	for _, entry := range reactions {
		declared.Reactions = append(declared.Reactions, configfile.Reaction{
			Name: entry.Name, Event: entry.EventType, Enabled: configfile.On(entry.Enabled),
			Match: nonEmpty(entry.Match), Plan: entry.Plan,
		})
	}

	schedules, err := e.Store.ListSchedules(ctx, project.ID)
	if err != nil {
		return declared, err
	}
	for _, entry := range schedules {
		declared.Schedules = append(declared.Schedules, configfile.Schedule{
			Name: entry.Name, Cron: entry.Cron, Timezone: entry.Timezone, Ref: entry.Ref,
			Enabled: configfile.On(entry.Enabled), Plan: entry.Plan,
		})
	}
	return declared, nil
}

func (e Exporter) descriptor(id string) (capability.Plugin, bool) {
	registry := capabilities.Of(e.Registry)
	if registry == nil {
		return capability.Plugin{}, false
	}
	descriptor, err := registry.Plugin(id)
	return descriptor, err == nil
}

// exportConfig writes a plugin's settings, its sensitive fields replaced by named references.
func exportConfig(
	descriptor capability.Plugin, scope plugin.Scope, config map[string]string,
	name func(field string) string, secrets map[string]bool,
) map[string]configfile.Value {
	if len(config) == 0 {
		return nil
	}
	out := map[string]configfile.Value{}
	for field, value := range config {
		if plugin.IsSecret(descriptor, scope, field) {
			env := name(field)
			secrets[env] = true
			out[field] = configfile.Value{Env: env}
			continue
		}
		out[field] = configfile.Value{Plain: value}
	}
	return out
}

var notEnv = regexp.MustCompile(`[^A-Z0-9]+`)

// secretName names the variable a sensitive setting is read from: ARK_<PLUGIN>_<FIELD> for the
// instance, ARK_<PROJECT>_<PLUGIN>_<FIELD> for a project.
func secretName(project, pluginID string) func(field string) string {
	return func(field string) string {
		parts := []string{"ARK"}
		if project != "" {
			parts = append(parts, project)
		}
		parts = append(parts, pluginID, field)
		name := strings.ToUpper(strings.Join(parts, "_"))
		return strings.Trim(notEnv.ReplaceAllString(name, "_"), "_")
	}
}

func sortedKeys[V any](entries map[string]V) []string {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func nonEmpty(match map[string]string) map[string]string {
	if len(match) == 0 {
		return nil
	}
	return match
}
