// Package configfile describes an instance's configuration as versioned YAML resources: one
// `Instance` for what holds across projects, one `Project` per project.
//
// It carries the shape and the structural checks, nothing that needs the database or the
// registry: whether a step names a kind of the catalogue or a fact an enabled plugin produces is
// checked by `internal/configure` when the configuration is applied.
//
// A sensitive setting is never written in a file. It is a [Value] reference, `{env: NAME}` or
// `{file: PATH}`, resolved when the configuration is applied, then sealed like a value typed into
// the screen.
package configfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/release"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/schedule"
)

// APIVersion versions the resources' shape.
const APIVersion = "arkhitekton.config/v1"

// The two kinds of resource.
const (
	KindInstance = "Instance"
	KindProject  = "Project"
)

// Set is a whole configuration: at most one instance, any number of projects.
type Set struct {
	Instance *Instance
	Projects []Project
}

// Project returns the project of this name, if the set declares it.
func (s Set) Project(name string) (Project, bool) {
	for _, project := range s.Projects {
		if project.Name == name {
			return project, true
		}
	}
	return Project{}, false
}

// Instance is what holds across projects.
type Instance struct {
	// Plugins are the instance's plugin settings, by plugin id.
	Plugins map[string]InstancePlugin `json:"plugins,omitempty"`
	// QueueLimits are the queues' ceilings, by kind. A kind absent keeps its catalogue ceiling.
	QueueLimits map[string]int `json:"queueLimits,omitempty"`
	// CapabilityBindings are the bindings recorded in the database, by capability ref, in the
	// catalogue file's shape. The catalogue file's own bindings stay in the file.
	CapabilityBindings map[string]json.RawMessage `json:"capabilityBindings,omitempty"`
}

// InstancePlugin is a plugin's settings for the whole instance.
type InstancePlugin struct {
	// Install names the artifact of an installed plugin. Absent for a plugin the binary carries.
	Install *Install `json:"install,omitempty"`
	// Enabled defaults to true: declaring a plugin is mostly to switch it on.
	Enabled *bool            `json:"enabled,omitempty"`
	Config  map[string]Value `json:"config,omitempty"`
}

// Install names the artifact a plugin is installed from.
type Install struct {
	// Reference is the plugin's repository, `registry/repository`, which the plugin then follows: a
	// newer version is offered on the Plugins screen. A digest (`@sha256:…`) or a tag pins it.
	Reference string `json:"reference"`
	// AcceptSource accepts a source the instance does not recognise, as the screen's button does.
	// Never implied: an unsigned artifact is installed only when this says so.
	AcceptSource bool `json:"acceptSource,omitempty"`
}

// Project is one project and everything configured for it.
type Project struct {
	// Name is the project's slug, from the resource's metadata.
	Name string `json:"-"`

	Repository     string        `json:"repository"`
	Label          string        `json:"label,omitempty"`
	ForgeURL       string        `json:"forgeUrl,omitempty"`
	Dockerfile     string        `json:"dockerfile,omitempty"`
	BuildContext   string        `json:"buildContext,omitempty"`
	ImageRegistry  string        `json:"imageRegistry,omitempty"`
	RegistrySecret string        `json:"registrySecret,omitempty"`
	VersionScheme  string        `json:"versionScheme,omitempty"`
	Stack          string        `json:"stack,omitempty"`
	TrackerProject string        `json:"trackerProject,omitempty"`
	MultiStage     bool          `json:"multiStage,omitempty"`
	Targets        []Target      `json:"targets,omitempty"`
	ReleaseKinds   release.Cycle `json:"releaseKinds,omitempty"`

	// Plugins are the project's own settings, by plugin id. A plugin absent inherits the
	// instance's settings.
	Plugins map[string]ProjectPlugin `json:"plugins,omitempty"`
	// Pipelines switch catalogue kinds on or off. A kind absent is on.
	Pipelines map[string]bool `json:"pipelines,omitempty"`
	// TrackerStatuses map the tracker's status names onto the dashboard's buckets.
	TrackerStatuses map[string]string `json:"trackerStatuses,omitempty"`
	// ReleaseAutomation absent leaves the automation as it is; with no stage, the project no
	// longer builds.
	ReleaseAutomation *plan.Plan `json:"releaseAutomation,omitempty"`
	Reactions         []Reaction `json:"reactions,omitempty"`
	Schedules         []Schedule `json:"schedules,omitempty"`
}

// Target is one image a project builds.
type Target struct {
	Stage      string `json:"stage,omitempty"`
	Image      string `json:"image"`
	Dockerfile string `json:"dockerfile,omitempty"`
	// Promote defaults to true: an image is promoted with its release unless it says otherwise.
	Promote *bool `json:"promote,omitempty"`
}

// ProjectPlugin is a project's decision about a plugin.
type ProjectPlugin struct {
	// Enabled defaults to true.
	Enabled *bool            `json:"enabled,omitempty"`
	Config  map[string]Value `json:"config,omitempty"`
}

// Reaction opens a plan when a fact is recorded. Its name identifies it within its project.
type Reaction struct {
	Name    string            `json:"name"`
	Event   string            `json:"event"`
	Enabled *bool             `json:"enabled,omitempty"`
	Match   map[string]string `json:"match,omitempty"`
	Plan    plan.Plan         `json:"plan"`
}

// Schedule opens a plan at the times a cron expression names. Its name identifies it within its
// project.
type Schedule struct {
	Name     string    `json:"name"`
	Cron     string    `json:"cron"`
	Timezone string    `json:"timezone,omitempty"`
	Ref      string    `json:"ref,omitempty"`
	Enabled  *bool     `json:"enabled,omitempty"`
	Plan     plan.Plan `json:"plan"`
}

// IsOn reads a switch that defaults to true.
func IsOn(flag *bool) bool { return flag == nil || *flag }

// On returns the switch to write for a state, omitted when it is the default.
func On(state bool) *bool {
	if state {
		return nil
	}
	return &state
}

// Value is a setting: a plain value, or a reference to where a sensitive one is read from.
type Value struct {
	Plain string
	// Env names the environment variable holding the value.
	Env string
	// File names the file holding the value.
	File string
}

// IsRef says whether the value is read from elsewhere.
func (v Value) IsRef() bool { return v.Env != "" || v.File != "" }

// MarshalJSON writes a plain value as a string, a reference as `{env: …}` or `{file: …}`.
func (v Value) MarshalJSON() ([]byte, error) {
	switch {
	case v.Env != "":
		return json.Marshal(map[string]string{"env": v.Env})
	case v.File != "":
		return json.Marshal(map[string]string{"file": v.File})
	}
	return json.Marshal(v.Plain)
}

// UnmarshalJSON reads a string, or a reference naming exactly one source.
func (v *Value) UnmarshalJSON(raw []byte) error {
	var plain string
	if err := json.Unmarshal(raw, &plain); err == nil {
		*v = Value{Plain: plain}
		return nil
	}
	var ref struct {
		Env  string `json:"env"`
		File string `json:"file"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ref); err != nil {
		return errors.New("expected a string, {env: NAME} or {file: PATH}")
	}
	if (ref.Env == "") == (ref.File == "") {
		return errors.New("a reference names exactly one of env and file")
	}
	*v = Value{Env: strings.TrimSpace(ref.Env), File: strings.TrimSpace(ref.File)}
	return nil
}

// namePattern is what a project's slug accepts, as the store derives it.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// envPattern is what an environment variable's name accepts.
var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// MaxQueueLimit mirrors the ceiling the screen accepts.
const MaxQueueLimit = 20

// Validate refuses what cannot be applied whatever the instance: malformed names, duplicates,
// plans or schedules that are wrong in themselves. Each problem names its path.
func (s Set) Validate() error {
	var problems []error
	if s.Instance != nil {
		problems = append(problems, s.Instance.validate())
	}
	seen := map[string]bool{}
	for _, project := range s.Projects {
		if seen[project.Name] {
			problems = append(problems, fmt.Errorf("project %q is declared twice", project.Name))
		}
		seen[project.Name] = true
		if err := project.validate(); err != nil {
			problems = append(problems, fmt.Errorf("project %q: %w", project.Name, err))
		}
	}
	return errors.Join(problems...)
}

func (i Instance) validate() error {
	var problems []error
	for id, entry := range i.Plugins {
		// The repository is read when the plugin is installed; here only its shape, `registry/repository`.
		if entry.Install != nil && !strings.Contains(strings.TrimSpace(entry.Install.Reference), "/") {
			problems = append(problems, fmt.Errorf(
				"instance: spec.plugins.%s.install.reference: expected `registry/repository`, with a tag or `@sha256:…` to pin it", id))
		}
		problems = append(problems, validateConfig("instance: spec.plugins."+id+".config", entry.Config))
	}
	for kind, max := range i.QueueLimits {
		if max < 1 || max > MaxQueueLimit {
			problems = append(problems, fmt.Errorf("instance: spec.queueLimits.%s: expected between 1 and %d", kind, MaxQueueLimit))
		}
	}
	for ref, binding := range i.CapabilityBindings {
		if !json.Valid(binding) || strings.HasPrefix(strings.TrimSpace(string(binding)), "null") {
			problems = append(problems, fmt.Errorf("instance: spec.capabilityBindings.%s: expected an object", ref))
		}
	}
	return errors.Join(problems...)
}

func (p Project) validate() error {
	var problems []error
	if !namePattern.MatchString(p.Name) {
		problems = append(problems, fmt.Errorf("metadata.name: expected lowercase letters, digits and dashes"))
	}
	if strings.TrimSpace(p.Repository) == "" {
		problems = append(problems, errors.New("spec.repository is required"))
	}
	for index, target := range p.Targets {
		if strings.TrimSpace(target.Image) == "" {
			problems = append(problems, fmt.Errorf("spec.targets[%d].image is required", index))
		}
	}
	for id, entry := range p.Plugins {
		problems = append(problems, validateConfig("spec.plugins."+id+".config", entry.Config))
	}
	if p.ReleaseAutomation != nil && len(p.ReleaseAutomation.Stages) > 0 {
		if err := p.ReleaseAutomation.Validate(); err != nil {
			problems = append(problems, fmt.Errorf("spec.releaseAutomation: %w", err))
		}
	}

	names := map[string]bool{}
	for index, entry := range p.Reactions {
		path := fmt.Sprintf("spec.reactions[%d]", index)
		key := nameKey(entry.Name)
		if names[key] {
			problems = append(problems, fmt.Errorf("%s: reaction %q is declared twice", path, entry.Name))
		}
		names[key] = true
		if strings.TrimSpace(entry.Event) == "" {
			problems = append(problems, fmt.Errorf("%s.event is required", path))
		}
		if strings.TrimSpace(entry.Name) == "" {
			problems = append(problems, fmt.Errorf("%s.name is required", path))
		}
		if err := entry.Plan.Validate(); err != nil {
			problems = append(problems, fmt.Errorf("%s.plan: %w", path, err))
		}
	}

	names = map[string]bool{}
	for index, entry := range p.Schedules {
		path := fmt.Sprintf("spec.schedules[%d]", index)
		key := nameKey(entry.Name)
		if names[key] {
			problems = append(problems, fmt.Errorf("%s: schedule %q is declared twice", path, entry.Name))
		}
		names[key] = true
		if err := entry.Schedule(0).Validate(); err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", path, err))
		}
	}
	return errors.Join(problems...)
}

// Schedule returns the declared schedule in the shape the store records.
func (s Schedule) Schedule(projectID int64) schedule.Schedule {
	return schedule.Schedule{
		ProjectID: projectID, Name: strings.TrimSpace(s.Name), Cron: strings.TrimSpace(s.Cron),
		Timezone: strings.TrimSpace(s.Timezone), Ref: strings.TrimSpace(s.Ref),
		Enabled: IsOn(s.Enabled), Plan: s.Plan,
	}
}

// NameKey is how reactions and schedules are told apart by name: trimmed and case-insensitive, as
// the database's unique index compares them.
func NameKey(name string) string { return nameKey(name) }

func nameKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

func validateConfig(path string, config map[string]Value) error {
	var problems []error
	for name, value := range config {
		if value.Env != "" && !envPattern.MatchString(value.Env) {
			problems = append(problems, fmt.Errorf("%s.%s.env: %q is not a variable name", path, name, value.Env))
		}
	}
	return errors.Join(problems...)
}
