package worker

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/action"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

/*
 * Running the actions a plugin declares.
 *
 * # What this file adds to the previous one
 *
 * `actions.go` carries out the outgoing call: a target typed into a form, a transport that
 * judges it, a named token. Here, none of that. A plugin action speaks to **its** boundary, the
 * one the plugin has already set — and that is what makes the two paths irreducible to each
 * other.
 *
 * Three consequences are worth naming:
 *
 *   - **the target is not chosen**. It comes from the plugin's configuration, not from the
 *     step. No address check is therefore made here: the one in `internal/webhook` exists
 *     because a URL comes from a form, and that is not the case;
 *   - **no secret is named**. The token is the plugin's, opened for the duration of the call. A
 *     step cannot name another token, and therefore cannot use one to extract a neighbour's;
 *   - **the plugin must be switched on**. A switched-off plugin is a named refusal, not a
 *     silent call: the configuration still exists in the database, and starting with it would
 *     send a status in the name of an integration one believes disabled.
 */

// PluginActionStore is what the plugin actions need from the database.
//
// Two reads and not one more: the plugin's resolved connection, and the project — of which
// only the repository is wanted. A wider interface would invite this runner to decide things
// that are not its own.
type PluginActionStore interface {
	ResolvedPlugin(ctx context.Context, projectID int64, pluginID string) (store.PluginSettings, error)
	GetProject(ctx context.Context, id int64) (store.Project, error)
}

// PluginActionRunner carries out the actions the plugins declare.
type PluginActionRunner struct {
	registry capabilities.Source
	store    PluginActionStore
	secrets  SecretOpener
	timeout  time.Duration
	logger   *slog.Logger
}

func NewPluginActionRunner(
	registry capabilities.Source, settings PluginActionStore, secrets SecretOpener,
	timeout time.Duration, logger *slog.Logger,
) *PluginActionRunner {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &PluginActionRunner{registry: registry, store: settings, secrets: secrets, timeout: timeout, logger: logger}
}

// Run carries out a plugin action step.
//
// Always returns a null job identifier, like the internal actions: none of this goes through
// the queue, and there is nothing to wait for.
func (r *PluginActionRunner) Run(
	ctx context.Context, run store.PlanRun, geste plan.Geste,
) (*int64, error) {
	descriptor, spec, found := capabilities.Of(r.registry).FindAction(geste.Ref)
	if !found {
		return nil, fmt.Errorf("no plugin declares the action %q", geste.Ref)
	}
	if run.ProjectID == nil {
		// A plugin is set per project. A fact naming none has no connection to borrow, and starting on
		// the instance's would pick a repository at random.
		return nil, fmt.Errorf(
			"the action %q bears on a project, and this fact names none", geste.Ref)
	}

	settings, err := r.store.ResolvedPlugin(ctx, *run.ProjectID, descriptor.ID)
	if err != nil {
		return nil, fmt.Errorf("settings of plugin %q: %w", descriptor.ID, err)
	}
	if !settings.Enabled {
		return nil, fmt.Errorf(
			"plugin %q is switched off for this project: the action %q does not start",
			descriptor.ID, geste.Ref)
	}

	config, err := openPluginSecrets(r.secrets, descriptor, settings.Config)
	if err != nil {
		return nil, fmt.Errorf("connection of plugin %q: %w", descriptor.ID, err)
	}

	project, err := r.store.GetProject(ctx, *run.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("project of the run: %w", err)
	}

	// The templates are bound **then** checked again. See [checkClosedSets]: a closed set
	// validated before interpolation says nothing about what will come out of it.
	params := bindParams(spec, geste.Params, run.Context)
	if err := checkClosedSets(spec, geste.Params, params); err != nil {
		return nil, err
	}

	if descriptor.Executor == nil {
		// A plugin that declares an action nobody carries out. The registry refuses it at start-up;
		// refusing it here too, by name, is better than reporting it "sent".
		return nil, fmt.Errorf("plugin %q declares %q, and nothing runs it", descriptor.ID, geste.Ref)
	}
	target := capability.Target{
		Project: project.Slug, Repository: project.Repository,
		Settings: capability.Settings(config), Timeout: r.timeout,
	}
	if err := descriptor.Executor.Execute(ctx, target, spec.Suffix, params); err != nil {
		return nil, err
	}
	r.logger.Info("plugin action carried out", "run", run.ID, "project", project.Slug, "action", geste.Ref)
	return nil, nil
}

// bindParams replaces the templates with the triggering fact's payload.
func bindParams(
	spec capability.ActionSpec, declares, runContext map[string]string,
) map[string]string {
	params := make(map[string]string, len(spec.Fields))
	for _, field := range spec.Fields {
		params[field.Name] = action.Expand(declares[field.Name], runContext)
	}
	return params
}

// checkClosedSets refuses an interpolated value outside the declared set.
//
// # Why this check could not happen earlier
//
// At recording time, a closed-set field lets a template through: what it will be worth depends
// on the fact's payload, and refusing it would force the state to be written literally. But
// the value **produced** has then been checked by nobody.
//
// It is a defect observed against a real forge, not a theoretical precaution. The natural
// template — `state: {outcome}` — yields `succeeded`, because that is a fact's vocabulary; the
// forge says `success`. Gitea **accepted** the call and stored the state as it was: a status
// outside its own vocabulary, which no interface can display, with not a single message.
//
// A lenient recipient is the worst case: nothing fails, and nothing works. The step therefore
// fails here, naming what is allowed.
//
// # Two vocabularies, and no translation
//
// A fact's outcome and a forge status's state do not correspond, and no table brings them
// together: it would have to be kept up, and would hold only for this forge. A reaction that
// wants to mirror the outcome is written with a literal state and a match on `outcome` — which
// the model already knows how to do.
func checkClosedSets(spec capability.ActionSpec, declares, params map[string]string) error {
	for _, field := range spec.Fields {
		if len(field.OneOf) == 0 {
			continue
		}
		value := strings.TrimSpace(params[field.Name])
		if value == "" || slices.Contains(field.OneOf, value) {
			continue
		}

		// The original template is quoted when there was one: without it, `received "succeeded"` does
		// not teach where the value comes from.
		origin := strings.TrimSpace(declares[field.Name])
		if origin != value {
			return fmt.Errorf("%s is %q after interpolating %q, "+
				"but the recipient allows only %s",
				field.Label, value, origin, strings.Join(field.OneOf, ", "))
		}
		return fmt.Errorf("%s is %q, but the recipient allows only %s",
			field.Label, value, strings.Join(field.OneOf, ", "))
	}
	return nil
}
