package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/action"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/webhook"
)

/*
 * Running a plan's actions.
 *
 * # An action is concluded the moment it is done
 *
 * That is what tells it apart from a pipeline step: a pipeline step becomes a job and waits for
 * its run to conclude, whereas an action gives its verdict at once. The engine knows it by the
 * null value returned in place of a job identifier.
 *
 * # What the context supplies, and what it does not
 *
 * The templates are bound to the triggering fact's payload — `{tag}`, `{outcome}`. **Secrets
 * never enter it**: a sensitive field is named, and set as a header by this package without
 * ever crossing a template or a log. That is what stops a token ending up interpolated into a
 * URL an error message will copy.
 */

// SecretResolver returns the clear-text value of a named sensitive field.
//
// A narrow interface, deliberately poor: it can do only one thing, so the step that calls it
// can get nothing else out of it. The designation is written `plugin.field` —
// `redmine.api_key`.
type SecretResolver interface {
	ResolveSecret(ctx context.Context, projectID *int64, reference string) (string, error)
}

// ActionRunner carries out a plan's actions.
//
// The core's own; a plugin's are handed to the dedicated runner, whose dependencies have
// nothing in common with these (see `pluginactions.go`). The entry point stays single because
// the check that precedes it — resolving the descriptor and revalidating the parameters —
// holds for both.
type ActionRunner struct {
	webhooks *webhook.Client
	secrets  SecretResolver
	plugins  GesteRunner
	logger   *slog.Logger
}

func NewActionRunner(
	webhooks *webhook.Client, secrets SecretResolver, logger *slog.Logger,
) *ActionRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &ActionRunner{webhooks: webhooks, secrets: secrets, logger: logger}
}

// WithPlugins wires the implementation of the actions plugins declare.
//
// Optional, and its absence says so: a deployment assembled without it refuses the step by
// name rather than reporting it as "sent".
func (r *ActionRunner) WithPlugins(plugins GesteRunner) *ActionRunner {
	r.plugins = plugins
	return r
}

// Run carries out an action step.
//
// Always returns a null job identifier: an action does not go through the queue, and has
// nothing to wait for.
func (r *ActionRunner) Run(
	ctx context.Context, run store.PlanRun, geste plan.Geste,
) (*int64, error) {
	descriptor, err := action.Get(geste.Ref)
	if err != nil {
		return nil, err
	}
	// Revalidated here although the plan was validated when recorded: a plan frozen into a run
	// may have been written by an earlier version of the registry, and starting it on parameters
	// an action no longer declares would produce a call nobody could describe.
	if err := descriptor.Validate(geste.Params); err != nil {
		return nil, err
	}

	// A plugin action leaves by the other path. The split is read in the descriptor rather than
	// in a split of the reference: `forge.status`, an internal action of an earlier version,
	// looks exactly like an action of the `forge` plugin.
	if descriptor.Plugin != "" {
		if r.plugins == nil {
			return nil, fmt.Errorf(
				"the actions of plugin %q are not configured", descriptor.Plugin)
		}
		return r.plugins.Run(ctx, run, geste)
	}

	switch geste.Ref {
	case action.Webhook:
		return nil, r.postWebhook(ctx, run, geste)
	default:
		// An action the registry declares and nothing runs. Refusing it by name is better than
		// reporting it "sent": the two have diverged, and saying so is the only way to notice.
		return nil, fmt.Errorf("action %q declared but not run yet", geste.Ref)
	}
}

// postWebhook issues the outgoing call of a `webhook` step.
func (r *ActionRunner) postWebhook(
	ctx context.Context, run store.PlanRun, geste plan.Geste,
) error {
	if r.webhooks == nil {
		return fmt.Errorf("outgoing calls not configured")
	}

	target := action.Expand(geste.Params["url"], run.Context)
	body, err := webhookBody(geste.Params["body"], run)
	if err != nil {
		return err
	}

	headers, err := r.authHeader(ctx, run, geste)
	if err != nil {
		return err
	}

	response, err := r.webhooks.Post(ctx, target, headers, body)
	if err != nil {
		return err
	}
	if !response.OK() {
		// A refusal from the recipient is an **observation**: the step fails, and the reason carries
		// the code and an excerpt. Without them, "failed" teaches nothing and forces a re-run to see.
		return fmt.Errorf("the recipient answered %d: %s",
			response.Status, truncate(response.Body, 200))
	}

	r.logger.Info("appel sortant abouti",
		"run", run.ID, "step", geste.Ref, "code", response.Status)
	return nil
}

// webhookBody composes the payload sent.
//
// An empty body sends the **fact's payload** as it is. That is the useful default: a generic
// recipient receives everything known, and nobody has a template to write to begin with.
func webhookBody(template string, run store.PlanRun) ([]byte, error) {
	if strings.TrimSpace(template) == "" {
		payload := map[string]string{}
		maps.Copy(payload, run.Context)
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("charge illisible: %w", err)
		}
		return encoded, nil
	}

	expanded := action.Expand(template, run.Context)
	// The body is announced as `application/json`: sending it unchecked would hand the recipient
	// invalid JSON, which it would reject with a message about **its** syntax rather than about
	// our template.
	if !json.Valid([]byte(expanded)) {
		return nil, fmt.Errorf("the body produced is not valid JSON")
	}
	return []byte(expanded), nil
}

// authHeader composes the authentication header, without ever returning the token.
//
// It is the only path by which a secret reaches an outgoing call. It passes through no
// template, no body, no log — the value lives only in the map returned, consumed immediately
// by the client.
func (r *ActionRunner) authHeader(
	ctx context.Context, run store.PlanRun, geste plan.Geste,
) (map[string]string, error) {
	name := strings.TrimSpace(geste.Params["auth_header"])
	reference := strings.TrimSpace(geste.Params["auth_secret"])

	switch {
	case name == "" && reference == "":
		return nil, nil
	case name == "" || reference == "":
		// The two go together. A token with no header would go nowhere; a header with no token would
		// go empty, and the recipient would refuse with an authentication message that would not say
		// the value is missing on our side.
		return nil, fmt.Errorf(
			"the authentication header and the token are declared together")
	case r.secrets == nil:
		return nil, fmt.Errorf("sealing is not configured: token %q "+
			"cannot be opened", reference)
	}

	value, err := r.secrets.ResolveSecret(ctx, run.ProjectID, reference)
	if err != nil {
		// The message does not quote the value, and for good reason: that is the one we could not
		// open.
		return nil, fmt.Errorf("jeton %q illisible: %w", reference, err)
	}
	if value == "" {
		return nil, fmt.Errorf("token %q is not set", reference)
	}
	return map[string]string{name: value}, nil
}

// truncate borne un extrait de réponse.
func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

// stepRouter sends a step to whoever can carry it out.
//
// The engine knows only a `GesteRunner`; the two kinds of step having nothing in common, the
// routing lives here rather than in the engine — which would then have no reason left to
// ignore the difference.
type stepRouter struct {
	pipelines GesteRunner
	actions   GesteRunner
}

func (r stepRouter) Run(
	ctx context.Context, run store.PlanRun, geste plan.Geste,
) (*int64, error) {
	// Both branches keep their runner. An engine assembled without one of them — which happens in
	// the tests, and would happen in a deployment where outgoing calls are not configured — must
	// refuse by name rather than dereference a nil interface: the step fails with its reason, and
	// the run concludes instead of bringing down the observation loop.
	switch geste.Kind {
	case plan.GestePipeline:
		if r.pipelines == nil {
			return nil, fmt.Errorf("pipeline chaining not configured")
		}
		return r.pipelines.Run(ctx, run, geste)
	case plan.GesteAction:
		if r.actions == nil {
			return nil, fmt.Errorf("actions not configured")
		}
		return r.actions.Run(ctx, run, geste)
	default:
		return nil, fmt.Errorf("unknown step kind %q", geste.Kind)
	}
}

// ActionIDs lists the available actions, for validating a plan.
func ActionIDs() []string {
	registry := action.All()
	ids := make([]string, 0, len(registry))
	for _, descriptor := range registry {
		ids = append(ids, descriptor.ID)
	}
	sort.Strings(ids)
	return ids
}
