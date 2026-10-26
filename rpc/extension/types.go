package extension

import (
	"errors"
	"strings"
	"time"
)

// The dashboard's data a plugin reads, as the Host service returns it.
type (
	Project struct {
		ID            int64
		Slug          string
		Repository    string
		Label         string
		ForgeURL      string
		Dockerfile    string
		BuildContext  string
		ImageRegistry string
		Stack         string
		VersionScheme string
		Targets       []Target
		MultiStage    bool
	}

	Target struct {
		Stage      string
		Image      string
		Dockerfile string
		// Promote is nil for an entry written before the field existed: promoted.
		Promote *bool
	}

	Release struct {
		ID              int64
		ProjectID       int64
		VersionID       int64
		Tag             string
		Kind            string
		State           string
		ImageVersion    string
		SourceReleaseID *int64
		GitRef          string
		GitCommit       string
		CreatedBy       string
		CreatedAt       time.Time
		ReleasedAt      *time.Time
	}

	JobRequest struct {
		Kind        string
		Priority    int
		Params      map[string]string
		RequestedBy string
		ReleaseID   *int64
		Environment *string
		ProjectID   *int64
	}

	// JobReleaseContext is a concluded job and the release it worked on.
	JobReleaseContext struct {
		ReleaseID   int64
		JobID       int64
		Kind        string
		Tag         string
		State       string
		Environment string
		GitRef      string
		GitCommit   string
		ProjectID   int64
	}

	// PluginSettings are the calling plugin's settings, resolved for a project and unsealed.
	PluginSettings struct {
		Enabled bool
		Config  map[string]string
	}

	// Environment is one a project declares in the catalogue, in promotion order.
	Environment struct {
		ID         string
		Label      string
		Rank       int
		Namespace  string
		Deployer   map[string]string
		Observable bool
		Visible    bool
		Sensitive  bool
	}

	Identity struct {
		Subject  string
		Username string
		Email    string
		Role     Role
		// ProjectRoles replace Role on their project.
		ProjectRoles map[int64]Role
	}

	// Suggestion is a value the launch form offers.
	Suggestion struct {
		Value string
		Label string
	}

	// Promotion is a release's promotion, as the release screen lists it.
	Promotion struct {
		Environment string
		Image       string
		Tag         string
		Digest      string
		JobID       *int64
		PushedAt    time.Time
	}

	// Event is a fact the plugin states. Type is the suffix its manifest declares; the dashboard
	// prefixes it with the plugin's id.
	Event struct {
		Type       string
		Subject    string
		OccurredAt time.Time
		Payload    map[string]string
		DedupKey   string
	}

	// ModuleState is what a module runs for: the instance's settings and the projects that switch
	// the plugin on, each with its resolved settings.
	ModuleState struct {
		Global   map[string]string
		Projects []ModuleProject
	}

	ModuleProject struct {
		Project  Project
		Settings map[string]string
	}
)

// Role is a role in the dashboard, from the least to the most.
type Role string

const (
	RoleViewer   Role = "viewer"
	RoleOperator Role = "operator"
	RoleReleaser Role = "releaser"
	RoleAdmin    Role = "admin"
)

// Permissions a plugin asks the Host about.
const (
	PermRead        = "read"
	PermReleaseEdit = "release:edit"
)

// PermDeploy is the permission to promote to an environment.
func PermDeploy(environment string) string { return "deploy:" + environment }

// Kinds and states a plugin names.
const (
	KindRetag         = "core/retag"
	ReleaseStateReady = "ready"
)

// The Host's errors a plugin tells apart with errors.Is.
var (
	ErrProjectNotFound = errors.New("project not found")
	ErrReleaseNotFound = errors.New("release not found")
	ErrPluginDisabled  = errors.New("plugin disabled for the project")
)

// Promoted says whether a re-tag propagates the image: an entry that says nothing is.
func (t Target) Promoted() bool { return t.Promote == nil || *t.Promote }

// DockerfileFor is the file that builds a target: its own, or the project's.
func (p Project) DockerfileFor(target Target) string {
	if file := strings.TrimSpace(target.Dockerfile); file != "" {
		return file
	}
	return p.Dockerfile
}

// Buildable are the targets a Dockerfile builds.
func (p Project) Buildable() []Target {
	var buildable []Target
	for _, target := range p.Targets {
		if strings.TrimSpace(p.DockerfileFor(target)) != "" {
			buildable = append(buildable, target)
		}
	}
	return buildable
}

// Promotable are the built targets a re-tag propagates.
func (p Project) Promotable() []Target {
	var promoted []Target
	for _, target := range p.Buildable() {
		if target.Promoted() {
			promoted = append(promoted, target)
		}
	}
	return promoted
}

// Reference is a target's repository, registry included; empty without a registry or an image.
func (p Project) Reference(target Target) string {
	registry := strings.Trim(strings.TrimSpace(p.ImageRegistry), "/")
	image := strings.TrimSpace(target.Image)
	if registry == "" || image == "" {
		return ""
	}
	return registry + "/" + image
}

// IsObservable says whether the dashboard can see the environment's cluster.
func (e Environment) IsObservable() bool { return e.Observable }

// IsVisible says whether the interface shows the environment.
func (e Environment) IsVisible() bool { return e.Visible }
