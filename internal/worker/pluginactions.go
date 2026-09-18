package worker

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/action"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/gitea"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
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
	store   PluginActionStore
	secrets SecretOpener
	timeout time.Duration
	logger  *slog.Logger
}

func NewPluginActionRunner(
	settings PluginActionStore, secrets SecretOpener, timeout time.Duration, logger *slog.Logger,
) *PluginActionRunner {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &PluginActionRunner{store: settings, secrets: secrets, timeout: timeout, logger: logger}
}

// Run carries out a plugin action step.
//
// Always returns a null job identifier, like the internal actions: none of this goes through
// the queue, and there is nothing to wait for.
func (r *PluginActionRunner) Run(
	ctx context.Context, run store.PlanRun, geste plan.Geste,
) (*int64, error) {
	descriptor, spec, found := plugin.FindAction(geste.Ref)
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

	config, err := openPluginSecrets(r.secrets, descriptor.ID, settings.Config)
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

	switch descriptor.ID {
	case "gitea":
		return nil, r.runGitea(ctx, run, geste.Ref, params, project, config)
	default:
		// A plugin that declares an action nobody carries out. Refusing it by name is better than
		// reporting it "sent": the registry and the runtime have diverged, and saying so is the only
		// way to notice.
		return nil, fmt.Errorf(
			"plugin %q declares %q, and nothing runs it", descriptor.ID, geste.Ref)
	}
}

// Full references of the Gitea plugin's actions.
const (
	giteaStatusRef  = "gitea.status"
	giteaCommentRef = "gitea.comment"
)

// bindParams replaces the templates with the triggering fact's payload.
func bindParams(
	spec plugin.ActionSpec, declares, contexte map[string]string,
) map[string]string {
	params := make(map[string]string, len(spec.Fields))
	for _, field := range spec.Fields {
		params[field.Name] = action.Expand(declares[field.Name], contexte)
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
func checkClosedSets(spec plugin.ActionSpec, declares, params map[string]string) error {
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
		origine := strings.TrimSpace(declares[field.Name])
		if origine != value {
			return fmt.Errorf("%s is %q after interpolating %q, "+
				"but the recipient allows only %s",
				field.Label, value, origine, strings.Join(field.OneOf, ", "))
		}
		return fmt.Errorf("%s is %q, but the recipient allows only %s",
			field.Label, value, strings.Join(field.OneOf, ", "))
	}
	return nil
}

// runGitea carries out the forge's actions.
func (r *PluginActionRunner) runGitea(
	ctx context.Context, run store.PlanRun, ref string,
	params map[string]string, project store.Project, config map[string]string,
) error {
	url := strings.TrimSpace(config["url"])
	token := strings.TrimSpace(config["token"])
	if url == "" || token == "" {
		// Named rather than left to the client: "invalid URL" would send someone hunting for a typo
		// where it is a setting that was never filled in.
		return fmt.Errorf("forge not configured: set the URL and the token of plugin \"gitea\"")
	}
	if strings.TrimSpace(project.Repository) == "" {
		return fmt.Errorf("project %q declares no repository", project.Slug)
	}

	writer, err := gitea.NewWriter(gitea.Options{
		URL: url, Token: token, Repository: project.Repository, Timeout: r.timeout,
	})
	if err != nil {
		return err
	}

	switch ref {
	case giteaStatusRef:
		commit := params["commit"]
		if commit == "" {
			return fmt.Errorf("the commit targeted is empty after interpolation: " +
				"the triggering fact does not carry the named variable")
		}
		if err := writer.SetCommitStatus(ctx, commit, params["state"],
			params["context"], params["description"], params["target_url"]); err != nil {
			return err
		}
		r.logger.Info("commit status set",
			"run", run.ID, "project", project.Slug,
			"contexte", params["context"], "state", params["state"])
		return nil

	case giteaCommentRef:
		raw := strings.TrimSpace(params["number"])
		number, err := strconv.Atoi(raw)
		if err != nil {
			// The common case, and it is worth saying this way: a reaction wired to `release.built` has no
			// `{number}`, the template stays literal, and "invalid" without quoting the value would send
			// someone hunting elsewhere.
			return fmt.Errorf("the pull request number is %q after "+
				"interpolation: does this fact really carry the named variable?", raw)
		}
		if err := writer.CommentOnPullRequest(ctx, number, params["body"]); err != nil {
			return err
		}
		r.logger.Info("comment posted",
			"run", run.ID, "project", project.Slug, "request", number)
		return nil

	default:
		return fmt.Errorf("action %q unknown to plugin \"gitea\"", ref)
	}
}
