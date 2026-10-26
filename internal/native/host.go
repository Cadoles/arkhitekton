package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/pluginv1"
)

// HostStore is what the Host service reads and writes of the dashboard's data.
type HostStore interface {
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	GetProject(ctx context.Context, id int64) (store.Project, error)
	GetRelease(ctx context.Context, projectID int64, tag string) (store.Release, error)
	Enqueue(ctx context.Context, request store.JobRequest) (int64, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
	ResolvedPlugin(ctx context.Context, projectID int64, pluginID string) (store.PluginSettings, error)
}

// HostDeps are what the Host service is built from, shared by every plugin of a role.
type HostDeps struct {
	Store    HostStore
	Catalogs catalog.Source
	// Authorizer answers Authorize; nil refuses everything, which is the worker's case: its plugins
	// serve no route.
	Authorizer *auth.Authorizer
	// Unseal opens a plugin's sensitive settings.
	Unseal func(pluginID string, sealed map[string]string) (map[string]string, error)
}

// NewHost returns the Host service lent to one plugin.
func NewHost(deps HostDeps, pluginID string) pluginv1.HostServer {
	return &host{deps: deps, pluginID: pluginID}
}

type host struct {
	pluginv1.UnimplementedHostServer
	deps     HostDeps
	pluginID string
}

func (h *host) ProjectBySlug(ctx context.Context, request *pluginv1.ProjectSlug) (*pluginv1.Project, error) {
	project, err := h.deps.Store.ProjectBySlug(ctx, request.GetSlug())
	if err != nil {
		return nil, storeStatus(err)
	}
	return ProjectTo(project), nil
}

func (h *host) GetProject(ctx context.Context, request *pluginv1.ProjectRef) (*pluginv1.Project, error) {
	project, err := h.deps.Store.GetProject(ctx, request.GetProjectId())
	if err != nil {
		return nil, storeStatus(err)
	}
	return ProjectTo(project), nil
}

func (h *host) GetRelease(ctx context.Context, request *pluginv1.ReleaseByTag) (*pluginv1.Release, error) {
	release, err := h.deps.Store.GetRelease(ctx, request.GetProjectId(), request.GetTag())
	if err != nil {
		return nil, storeStatus(err)
	}
	return ReleaseTo(release), nil
}

func (h *host) Enqueue(ctx context.Context, request *pluginv1.JobRequest) (*pluginv1.JobID, error) {
	id, err := h.deps.Store.Enqueue(ctx, store.JobRequest{
		Kind: request.GetKind(), Priority: int(request.GetPriority()), Params: request.GetParams(),
		RequestedBy: request.GetRequestedBy(), ReleaseID: request.ReleaseId,
		Environment: request.Environment, ProjectID: request.ProjectId,
	})
	if err != nil {
		return nil, storeStatus(err)
	}
	return &pluginv1.JobID{Id: id}, nil
}

func (h *host) Audit(ctx context.Context, entry *pluginv1.AuditEntry) (*pluginv1.Empty, error) {
	var payload map[string]any
	if raw := entry.GetPayload(); len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "audit payload: %v", err)
		}
	}
	if err := h.deps.Store.Audit(ctx, entry.GetActor(), entry.GetAction(), entry.GetTarget(), payload); err != nil {
		return nil, storeStatus(err)
	}
	return &pluginv1.Empty{}, nil
}

func (h *host) ResolvedPlugin(ctx context.Context, request *pluginv1.ProjectRef) (*pluginv1.PluginSettings, error) {
	settings, err := h.deps.Store.ResolvedPlugin(ctx, request.GetProjectId(), h.pluginID)
	if err != nil {
		return nil, storeStatus(err)
	}
	config := settings.Config
	if h.deps.Unseal != nil {
		if config, err = h.deps.Unseal(h.pluginID, config); err != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "opening the settings: %v", err)
		}
	}
	return &pluginv1.PluginSettings{Enabled: settings.Enabled, Config: config}, nil
}

func (h *host) Environments(ctx context.Context, request *pluginv1.ProjectRef) (*pluginv1.EnvironmentList, error) {
	project, err := h.deps.Store.GetProject(ctx, request.GetProjectId())
	if err != nil {
		return nil, storeStatus(err)
	}
	list := &pluginv1.EnvironmentList{}
	for _, environment := range catalog.Of(h.deps.Catalogs).EnvironmentsOf(project.Slug) {
		list.Environments = append(list.Environments, &pluginv1.Environment{
			Id: environment.ID, Label: environment.Label, Rank: int32(environment.Rank),
			Namespace: environment.Namespace, Deployer: environment.Deployer,
			Observable: environment.IsObservable(), Visible: environment.IsVisible(),
			Sensitive: environment.Sensitive == nil || *environment.Sensitive,
		})
	}
	return list, nil
}

func (h *host) Authorize(ctx context.Context, request *pluginv1.Authorization) (*pluginv1.Decision, error) {
	if h.deps.Authorizer == nil {
		return &pluginv1.Decision{}, nil
	}
	identity := IdentityFrom(request.GetIdentity())
	permission := auth.Permission(request.GetPermission())
	if environment, found := deployTarget(permission); found {
		project, err := h.deps.Store.GetProject(ctx, request.GetProjectId())
		if err != nil {
			return nil, storeStatus(err)
		}
		return &pluginv1.Decision{Allowed: h.deps.Authorizer.CanDeploy(identity, project.ID, project.Slug, environment)}, nil
	}
	return &pluginv1.Decision{Allowed: h.deps.Authorizer.CanIn(identity, request.GetProjectId(), permission)}, nil
}

// deployTarget returns the environment a deploy permission names.
func deployTarget(permission auth.Permission) (string, bool) {
	const prefix = "deploy:"
	if len(permission) > len(prefix) && string(permission[:len(prefix)]) == prefix {
		return string(permission[len(prefix):]), true
	}
	return "", false
}

// storeStatus says which error it was, the way the plugin's side reads it back.
func storeStatus(err error) error {
	switch {
	case errors.Is(err, store.ErrProjectNotFound):
		return status.Error(codes.NotFound, "project not found")
	case errors.Is(err, store.ErrReleaseNotFound):
		return status.Error(codes.NotFound, "release not found")
	case errors.Is(err, store.ErrPluginDisabled):
		return status.Error(codes.FailedPrecondition, "plugin disabled for the project")
	}
	return status.Error(codes.Internal, err.Error())
}

// ProjectTo is a project as a plugin receives it.
func ProjectTo(project store.Project) *pluginv1.Project {
	targets := make([]*pluginv1.Target, 0, len(project.Targets))
	for _, target := range project.Targets {
		targets = append(targets, &pluginv1.Target{
			Stage: target.Stage, Image: target.Image, Dockerfile: target.Dockerfile, Promote: target.Promote,
		})
	}
	return &pluginv1.Project{
		Id: project.ID, Slug: project.Slug, Repository: project.Repository, Label: project.Label,
		ForgeUrl: project.ForgeURL, Dockerfile: project.Dockerfile, BuildContext: project.BuildContext,
		ImageRegistry: project.ImageRegistry, Stack: project.Stack, VersionScheme: project.VersionScheme,
		Targets: targets, MultiStage: project.MultiStage,
	}
}

// ReleaseTo is a release as a plugin receives it.
func ReleaseTo(release store.Release) *pluginv1.Release {
	released := ""
	if release.ReleasedAt != nil {
		released = formatTime(*release.ReleasedAt)
	}
	return &pluginv1.Release{
		Id: release.ID, ProjectId: release.ProjectID, VersionId: release.VersionID, Tag: release.Tag,
		Kind: release.Kind, State: release.State, ImageVersion: release.ImageVersion,
		SourceReleaseId: release.SourceReleaseID, GitRef: release.GitRef, GitCommit: release.GitCommit,
		CreatedBy: release.CreatedBy, CreatedAt: formatTime(release.CreatedAt), ReleasedAt: released,
	}
}

// IdentityTo is a caller as a plugin receives it.
func IdentityTo(identity auth.Identity) *pluginv1.Identity {
	projects := make(map[int64]string, len(identity.Projects))
	for id, role := range identity.Projects {
		projects[id] = string(role)
	}
	return &pluginv1.Identity{
		Subject: identity.Subject, Username: identity.Username, Email: identity.Email,
		Role: string(identity.Role), ProjectRoles: projects,
	}
}

// IdentityFrom reads back an identity a plugin hands the Host.
func IdentityFrom(identity *pluginv1.Identity) auth.Identity {
	projects := make(map[int64]auth.Role, len(identity.GetProjectRoles()))
	for id, role := range identity.GetProjectRoles() {
		projects[id] = auth.Role(role)
	}
	return auth.Identity{
		Subject: identity.GetSubject(), Username: identity.GetUsername(), Email: identity.GetEmail(),
		Role: auth.Role(identity.GetRole()), Projects: projects,
	}
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

// ParseTime reads a time a plugin sends.
func ParseTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("time %q: %w", value, err)
	}
	return parsed, nil
}
