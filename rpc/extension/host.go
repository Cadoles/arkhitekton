package extension

import (
	"context"
	"encoding/json"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"forge.cadoles.com/vcarroy/arkhitekton/rpc/pluginv1"
)

// Host is what the dashboard lends a plugin: its data, its queue, its audit log and its
// authorisation. The plugin's own tables it reaches through its own connection.
type Host interface {
	ProjectBySlug(ctx context.Context, slug string) (Project, error)
	GetProject(ctx context.Context, id int64) (Project, error)
	GetRelease(ctx context.Context, projectID int64, tag string) (Release, error)
	Enqueue(ctx context.Context, request JobRequest) (int64, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
	// ResolvedPlugin returns this plugin's settings for a project; ErrPluginDisabled when the
	// project switches it off.
	ResolvedPlugin(ctx context.Context, projectID int64) (PluginSettings, error)
	// Environments returns the environments a project declares in the catalogue.
	Environments(ctx context.Context, projectID int64) ([]Environment, error)
	// Authorize says whether an identity holds a permission in a project.
	Authorize(ctx context.Context, identity Identity, projectID int64, permission string) (bool, error)
	// ReleaseDossier returns what the dashboard records about a release. The api serves it.
	ReleaseDossier(ctx context.Context, projectID int64, tag string) (Dossier, error)
}

type hostClient struct{ client pluginv1.HostClient }

func (h hostClient) ProjectBySlug(ctx context.Context, slug string) (Project, error) {
	project, err := h.client.ProjectBySlug(ctx, &pluginv1.ProjectSlug{Slug: slug})
	if err != nil {
		return Project{}, hostError(err)
	}
	return projectFrom(project), nil
}

func (h hostClient) GetProject(ctx context.Context, id int64) (Project, error) {
	project, err := h.client.GetProject(ctx, &pluginv1.ProjectRef{ProjectId: id})
	if err != nil {
		return Project{}, hostError(err)
	}
	return projectFrom(project), nil
}

func (h hostClient) GetRelease(ctx context.Context, projectID int64, tag string) (Release, error) {
	release, err := h.client.GetRelease(ctx, &pluginv1.ReleaseByTag{ProjectId: projectID, Tag: tag})
	if err != nil {
		return Release{}, hostError(err)
	}
	return releaseFrom(release), nil
}

func (h hostClient) Enqueue(ctx context.Context, request JobRequest) (int64, error) {
	job, err := h.client.Enqueue(ctx, &pluginv1.JobRequest{
		Kind: request.Kind, Priority: int32(request.Priority), Params: request.Params,
		RequestedBy: request.RequestedBy, ReleaseId: request.ReleaseID,
		Environment: request.Environment, ProjectId: request.ProjectID,
	})
	if err != nil {
		return 0, hostError(err)
	}
	return job.GetId(), nil
}

func (h hostClient) Audit(ctx context.Context, actor, action, target string, payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = h.client.Audit(ctx, &pluginv1.AuditEntry{Actor: actor, Action: action, Target: target, Payload: encoded})
	return hostError(err)
}

func (h hostClient) ResolvedPlugin(ctx context.Context, projectID int64) (PluginSettings, error) {
	settings, err := h.client.ResolvedPlugin(ctx, &pluginv1.ProjectRef{ProjectId: projectID})
	if err != nil {
		return PluginSettings{}, hostError(err)
	}
	return PluginSettings{Enabled: settings.GetEnabled(), Config: settings.GetConfig()}, nil
}

func (h hostClient) Environments(ctx context.Context, projectID int64) ([]Environment, error) {
	list, err := h.client.Environments(ctx, &pluginv1.ProjectRef{ProjectId: projectID})
	if err != nil {
		return nil, hostError(err)
	}
	environments := make([]Environment, 0, len(list.GetEnvironments()))
	for _, environment := range list.GetEnvironments() {
		environments = append(environments, Environment{
			ID: environment.GetId(), Label: environment.GetLabel(), Rank: int(environment.GetRank()),
			Namespace: environment.GetNamespace(), Deployer: environment.GetDeployer(),
			Observable: environment.GetObservable(), Visible: environment.GetVisible(),
			Sensitive: environment.GetSensitive(),
		})
	}
	return environments, nil
}

func (h hostClient) Authorize(ctx context.Context, identity Identity, projectID int64, permission string) (bool, error) {
	decision, err := h.client.Authorize(ctx, &pluginv1.Authorization{
		Identity: identityTo(identity), ProjectId: projectID, Permission: permission,
	})
	if err != nil {
		return false, hostError(err)
	}
	return decision.GetAllowed(), nil
}

func (h hostClient) ReleaseDossier(ctx context.Context, projectID int64, tag string) (Dossier, error) {
	dossier, err := h.client.GetReleaseDossier(ctx, &pluginv1.ReleaseByTag{ProjectId: projectID, Tag: tag})
	if err != nil {
		return Dossier{}, hostError(err)
	}
	return dossierFrom(dossier), nil
}

// hostError gives back the errors a plugin tells apart. The Host says which by its status: not
// found with the error's text, failed precondition for a plugin switched off.
func hostError(err error) error {
	if err == nil {
		return nil
	}
	state, ok := status.FromError(err)
	if !ok {
		return err
	}
	switch {
	case state.Code() == codes.NotFound && state.Message() == ErrProjectNotFound.Error():
		return ErrProjectNotFound
	case state.Code() == codes.NotFound && state.Message() == ErrReleaseNotFound.Error():
		return ErrReleaseNotFound
	case state.Code() == codes.FailedPrecondition && state.Message() == ErrPluginDisabled.Error():
		return ErrPluginDisabled
	}
	return err
}

func projectFrom(project *pluginv1.Project) Project {
	if project == nil {
		return Project{}
	}
	targets := make([]Target, 0, len(project.GetTargets()))
	for _, target := range project.GetTargets() {
		targets = append(targets, Target{
			Stage: target.GetStage(), Image: target.GetImage(), Dockerfile: target.GetDockerfile(),
			Promote: target.Promote,
		})
	}
	return Project{
		ID: project.GetId(), Slug: project.GetSlug(), Repository: project.GetRepository(),
		Label: project.GetLabel(), ForgeURL: project.GetForgeUrl(), Dockerfile: project.GetDockerfile(),
		BuildContext: project.GetBuildContext(), ImageRegistry: project.GetImageRegistry(),
		Stack: project.GetStack(), VersionScheme: project.GetVersionScheme(), Targets: targets,
		MultiStage: project.GetMultiStage(),
	}
}

func releaseFrom(release *pluginv1.Release) Release {
	return Release{
		ID: release.GetId(), ProjectID: release.GetProjectId(), VersionID: release.GetVersionId(),
		Tag: release.GetTag(), Kind: release.GetKind(), State: release.GetState(),
		ImageVersion: release.GetImageVersion(), SourceReleaseID: release.SourceReleaseId,
		GitRef: release.GetGitRef(), GitCommit: release.GetGitCommit(), CreatedBy: release.GetCreatedBy(),
		CreatedAt: parseTime(release.GetCreatedAt()), ReleasedAt: optionalTime(release.GetReleasedAt()),
	}
}

func identityFrom(identity *pluginv1.Identity) Identity {
	if identity == nil {
		return Identity{}
	}
	projects := make(map[int64]Role, len(identity.GetProjectRoles()))
	for id, role := range identity.GetProjectRoles() {
		projects[id] = Role(role)
	}
	return Identity{
		Subject: identity.GetSubject(), Username: identity.GetUsername(), Email: identity.GetEmail(),
		Role: Role(identity.GetRole()), ProjectRoles: projects,
	}
}

func identityTo(identity Identity) *pluginv1.Identity {
	projects := make(map[int64]string, len(identity.ProjectRoles))
	for id, role := range identity.ProjectRoles {
		projects[id] = string(role)
	}
	return &pluginv1.Identity{
		Subject: identity.Subject, Username: identity.Username, Email: identity.Email,
		Role: string(identity.Role), ProjectRoles: projects,
	}
}

func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

func optionalTime(value string) *time.Time {
	if value == "" {
		return nil
	}
	parsed := parseTime(value)
	return &parsed
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func dossierFrom(dossier *pluginv1.ReleaseDossier) Dossier {
	converted := Dossier{Version: dossier.GetVersion(), ArtifactsNote: dossier.GetArtifactsNote()}
	if actions := dossier.GetManualActions(); actions != nil {
		converted.ManualActions = &ManualActions{
			Markdown: actions.GetMarkdown(), UpdatedBy: actions.GetUpdatedBy(),
			UpdatedAt: optionalTime(actions.GetUpdatedAt()),
		}
	}
	for _, note := range dossier.GetNotes() {
		converted.Notes = append(converted.Notes, DossierNote{
			ExternalID: note.GetExternalId(), Subject: note.GetSubject(), Tracker: note.GetTracker(),
			Status: note.GetStatus(), Anomaly: note.GetAnomaly(), Known: note.GetKnown(),
			PullRequest: note.GetPullRequest(), URL: note.GetUrl(),
		})
	}
	for _, artifact := range dossier.GetArtifacts() {
		converted.Artifacts = append(converted.Artifacts, DossierArtifact{
			Geste: artifact.GetGeste(), Name: artifact.GetName(), Kind: artifact.GetKind(),
			Reference: artifact.GetReference(), Digest: artifact.GetDigest(),
		})
	}
	for _, result := range dossier.GetResults() {
		converted.Results = append(converted.Results, DossierResult{
			Group: result.GetGroup(), Shape: result.GetShape(), Tool: result.GetTool(),
			Status: result.GetStatus(), Total: int(result.GetTotal()), Passed: int(result.GetPassed()),
			Failed: int(result.GetFailed()), Skipped: int(result.GetSkipped()), Flaky: int(result.GetFlaky()),
			DurationMS: result.GetDurationMs(), MeasuredAt: parseTime(result.GetMeasuredAt()),
			RunName: result.GetRunName(),
		})
	}
	score := dossier.GetScore()
	converted.Score = DossierScore{
		Value: int(score.GetValue()), Measured: score.GetMeasured(), Health: score.GetHealth(),
		Redistributed: score.GetRedistributed(),
	}
	for _, signal := range score.GetSignals() {
		converted.Score.Signals = append(converted.Score.Signals, DossierSignal{
			Key: signal.GetKey(), Label: signal.GetLabel(), Group: signal.GetGroup(),
			Value: signal.GetValue(), Weight: signal.GetWeight(), Measured: signal.GetMeasured(),
			Health: signal.GetHealth(), Detail: signal.GetDetail(),
		})
	}
	if audit := dossier.GetAudit(); audit != nil {
		converted.Audit = &DossierAudit{
			Measured: audit.GetMeasured(), FailedScanners: audit.GetFailedScanners(),
			Total: int(audit.GetTotal()), Critical: int(audit.GetCritical()), High: int(audit.GetHigh()),
			Medium: int(audit.GetMedium()), Low: int(audit.GetLow()), Suppressed: int(audit.GetSuppressed()),
			DeltaNew: int(audit.GetDeltaNew()), DeltaExisting: int(audit.GetDeltaExisting()),
			DeltaResolved: int(audit.GetDeltaResolved()), GeneratedAt: audit.GetGeneratedAt(),
			RunName: audit.GetRunName(),
		}
	}
	return converted
}
