package app

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/host"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	dashhttp "forge.cadoles.com/vcarroy/arkhitekton/internal/http"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/installed"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/native"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/pluginv1"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
)

/*
 * The extension hooks installed plugins answer, composed with the compiled extensions'.
 *
 * Read at each call among the active versions: installing or switching a plugin changes what the
 * release screen, the launch form or the scheduler hear, without a restart. A program that fails
 * is left out, logged, and the others still answer: one plugin down does not take a screen with
 * it.
 */

// hookTimeout bounds a program's answer to a hook: a screen waits for it.
const hookTimeout = 10 * time.Second

// pluginHooks asks the installed plugins' programs.
type pluginHooks struct {
	programs *programs
	db       interface {
		ResolvedPlugin(ctx context.Context, projectID int64, pluginID string) (store.PluginSettings, error)
		ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	}
	logger *slog.Logger
}

// answering returns the clients of the active plugins that declare a hook and are switched on, for
// a project or, with none, for the instance.
func (h pluginHooks) answering(ctx context.Context, hook manifest.Hook, projectID int64) map[string]pluginv1.PluginClient {
	clients := map[string]pluginv1.PluginClient{}
	for _, plugin := range h.programs.hooked(hook) {
		settings, err := h.db.ResolvedPlugin(ctx, projectID, plugin.ID)
		if err != nil || !settings.Enabled {
			continue
		}
		_, process, found := h.programs.activeProcess(plugin.ID)
		if !found {
			continue
		}
		conn, err := process.Connection(ctx)
		if err != nil {
			h.logger.Warn("plugin left out of a hook", "plugin", plugin.ID, "hook", hook, "error", err)
			continue
		}
		clients[plugin.ID] = conn.Plugin
	}
	return clients
}

func (h pluginHooks) failed(plugin string, hook manifest.Hook, err error) {
	h.logger.Warn("plugin left out of a hook", "plugin", plugin, "hook", hook, "error", err)
}

func (h pluginHooks) deployments(compiled func(context.Context, int64) (map[string][]string, error)) func(context.Context, int64) (map[string][]string, error) {
	return func(ctx context.Context, projectID int64) (map[string][]string, error) {
		merged := map[string][]string{}
		if compiled != nil {
			deployed, err := compiled(ctx, projectID)
			if err != nil {
				return nil, err
			}
			maps.Copy(merged, deployed)
		}
		ctx, cancel := context.WithTimeout(ctx, hookTimeout)
		defer cancel()
		for id, client := range h.answering(ctx, manifest.HookDeployments, projectID) {
			answer, err := client.Deployments(ctx, &pluginv1.ProjectRef{ProjectId: projectID})
			if err != nil {
				h.failed(id, manifest.HookDeployments, err)
				continue
			}
			for tag, environments := range answer.GetByTag() {
				merged[tag] = append(merged[tag], environments.GetIds()...)
			}
		}
		return merged, nil
	}
}

func (h pluginHooks) promotions(compiled func(context.Context, int64, int64) ([]host.ReleasePromotion, error)) func(context.Context, int64, int64) ([]host.ReleasePromotion, error) {
	return func(ctx context.Context, projectID, releaseID int64) ([]host.ReleasePromotion, error) {
		var all []host.ReleasePromotion
		if compiled != nil {
			promotions, err := compiled(ctx, projectID, releaseID)
			if err != nil {
				return nil, err
			}
			all = append(all, promotions...)
		}
		ctx, cancel := context.WithTimeout(ctx, hookTimeout)
		defer cancel()
		for id, client := range h.answering(ctx, manifest.HookPromotions, projectID) {
			answer, err := client.Promotions(ctx, &pluginv1.ReleaseRef{ProjectId: projectID, ReleaseId: releaseID})
			if err != nil {
				h.failed(id, manifest.HookPromotions, err)
				continue
			}
			for _, promotion := range answer.GetPromotions() {
				pushed, _ := native.ParseTime(promotion.GetPushedAt())
				all = append(all, dashhttp.ReleasePromotion{
					Environment: promotion.GetEnvironment(), Image: promotion.GetImage(), Tag: promotion.GetTag(),
					Digest: promotion.GetDigest(), JobID: promotion.JobId, PushedAt: pushed,
				})
			}
		}
		return all, nil
	}
}

// suggestions answers nil when nobody offers anything: the field then stays free text.
func (h pluginHooks) suggestions(compiled func(context.Context, string) []host.Suggestion) func(context.Context, string) []host.Suggestion {
	return func(ctx context.Context, slug string) []host.Suggestion {
		var all []host.Suggestion
		if compiled != nil {
			all = append(all, compiled(ctx, slug)...)
		}
		project, err := h.db.ProjectBySlug(ctx, slug)
		if err != nil {
			return all
		}
		ctx, cancel := context.WithTimeout(ctx, hookTimeout)
		defer cancel()
		for id, client := range h.answering(ctx, manifest.HookEnvironmentSuggestions, project.ID) {
			answer, err := client.EnvironmentSuggestions(ctx, &pluginv1.ProjectSlug{Slug: slug})
			if err != nil {
				h.failed(id, manifest.HookEnvironmentSuggestions, err)
				continue
			}
			if !answer.GetOffered() {
				continue
			}
			for _, suggestion := range answer.GetSuggestions() {
				all = append(all, host.Suggestion{Value: suggestion.GetValue(), Label: suggestion.GetLabel()})
			}
		}
		return all
	}
}

// schemeCheck refuses a scheme any switched-on plugin refuses. A plugin that cannot answer does
// not refuse: it may be restarting, and saving a project must not wait for it.
func (h pluginHooks) schemeCheck(compiled func(context.Context, host.Scheme) error) func(context.Context, host.Scheme) error {
	return func(ctx context.Context, scheme host.Scheme) error {
		if compiled != nil {
			if err := compiled(ctx, scheme); err != nil {
				return err
			}
		}
		ctx, cancel := context.WithTimeout(ctx, hookTimeout)
		defer cancel()
		for id, client := range h.answering(ctx, manifest.HookSchemeCheck, 0) {
			answer, err := client.CheckScheme(ctx, &pluginv1.Scheme{Id: scheme.ID()})
			if err != nil {
				h.failed(id, manifest.HookSchemeCheck, err)
				continue
			}
			if reason := answer.GetReason(); reason != "" {
				return fmt.Errorf("plugin %s: %s", id, reason)
			}
		}
		return nil
	}
}

func (h pluginHooks) projectSettings(compiled func(context.Context, int64) (map[string]string, error)) func(context.Context, int64) (map[string]string, error) {
	return func(ctx context.Context, projectID int64) (map[string]string, error) {
		merged := map[string]string{}
		if compiled != nil {
			values, err := compiled(ctx, projectID)
			if err != nil {
				return nil, err
			}
			maps.Copy(merged, values)
		}
		ctx, cancel := context.WithTimeout(ctx, hookTimeout)
		defer cancel()
		for id, client := range h.answering(ctx, manifest.HookProjectSettings, projectID) {
			answer, err := client.ProjectSettings(ctx, &pluginv1.ProjectRef{ProjectId: projectID})
			if err != nil {
				// Left out, the run would act on the instance's values: refused rather than misdirected.
				return nil, fmt.Errorf("plugin %s: the project's settings: %w", id, err)
			}
			maps.Copy(merged, answer.GetValues())
		}
		return merged, nil
	}
}

// pluginPromotionFacts states a concluded promotion through the one plugin that does: a compiled
// extension, or else the installed plugin that declares the hook.
type pluginPromotionFacts struct {
	hooks    pluginHooks
	compiled host.PromotionFacts
}

func (f pluginPromotionFacts) Promoted(at time.Time, linked store.JobReleaseContext) event.Event {
	if f.compiled != nil {
		return f.compiled.Promoted(at, linked)
	}
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	for id, client := range f.hooks.answering(ctx, manifest.HookPromotionFacts, linked.ProjectID) {
		answer, err := client.Promoted(ctx, &pluginv1.PromotedRequest{
			OccurredAt: at.UTC().Format(time.RFC3339Nano), ReleaseId: linked.ReleaseID, JobId: linked.JobID,
			Kind: linked.Kind, Tag: linked.Tag, State: linked.State, Environment: linked.Environment,
			GitRef: linked.GitRef, GitCommit: linked.GitCommit, ProjectId: linked.ProjectID,
		})
		if err != nil {
			f.hooks.failed(id, manifest.HookPromotionFacts, err)
			continue
		}
		if !answer.GetStated() {
			continue
		}
		occurred, err := native.ParseTime(answer.GetOccurredAt())
		if err != nil || occurred.IsZero() {
			occurred = at
		}
		kind := event.Type(id + "." + answer.GetType())
		dedup := answer.GetDedupKey()
		if dedup == "" {
			dedup = fmt.Sprintf("%s:%d", kind, linked.JobID)
		}
		projectID := linked.ProjectID
		return event.Event{
			Type: kind, Source: id, ProjectID: &projectID, Subject: answer.GetSubject(),
			Payload: answer.GetPayload(), OccurredAt: occurred, DedupKey: dedup,
		}
	}
	return event.Event{}
}

// hooked returns the active versions whose program answers a hook.
func (p *programs) hooked(hook manifest.Hook) []installed.Plugin {
	p.mu.Lock()
	defer p.mu.Unlock()
	var plugins []installed.Plugin
	for _, plugin := range p.active {
		for _, declared := range plugin.Native.Manifest.Hooks {
			if declared == hook {
				plugins = append(plugins, plugin)
			}
		}
	}
	return plugins
}
