package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/deploykeys"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The loop that supplies the projects with deploy keys.
 *
 * It lives in the worker and not in the API, and that is no detail of distribution. The API
 * runs with `automountServiceAccountToken: false` — it has no cluster token, deliberately,
 * because it is the exposed surface. Granting it one for the convenience of setting a key
 * synchronously at declaration would be a security regression dressed as a feature.
 *
 * The API therefore **requests**, in the database; the worker carries out, under the leader's
 * lease. That is also what gives the reconciliation loop its purpose: a project with no key
 * receives one, a requested rotation runs, and a restart catches up what an outage interrupted.
 */

// keyInterval spaces two passes of the loop.
//
// Long, deliberately: setting a key is triggered by a rare human action — declaring a
// project, asking for a rotation — and not by a stream. Probing more often would multiply the
// requests to gain at best a few seconds on a step that already takes several.
const keyInterval = time.Minute

// KeyStore is what the loop needs from the database.
type KeyStore interface {
	ProjectsAwaitingKey(ctx context.Context) ([]store.PendingKey, error)
	SetDeployKey(ctx context.Context, projectID int64, public string) error
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// KeyIssuer puts a project's key into the cluster.
type KeyIssuer interface {
	Issue(ctx context.Context, slug string, known bool) (string, error)
}

// KeyReconciler supplies the projects waiting for a key.
type KeyReconciler struct {
	store  KeyStore
	keys   KeyIssuer
	logger *slog.Logger
}

func NewKeyReconciler(projects KeyStore, keys KeyIssuer, logger *slog.Logger) *KeyReconciler {
	return &KeyReconciler{store: projects, keys: keys, logger: logger}
}

// Run runs the loop until cancellation.
//
// A first pass immediately: a project declared while the worker was stopped must not wait for
// the next tick to receive its key.
func (r *KeyReconciler) Run(ctx context.Context) error {
	ticker := time.NewTicker(keyInterval)
	defer ticker.Stop()

	for {
		r.once(ctx)

		select {
		case <-ctx.Done():
			r.logger.Info("deploy-key setting stopped")
			return nil
		case <-ticker.C:
		}
	}
}

func (r *KeyReconciler) once(ctx context.Context) {
	pending, err := r.store.ProjectsAwaitingKey(ctx)
	if err != nil {
		r.logger.Error("projects waiting for a key", "error", err)
		return
	}

	for _, project := range pending {
		r.issue(ctx, project)
	}
}

// issue sets a project's key, and does not interrupt the others if it fails.
//
// A project whose Secret carries a name already taken is a case a human must settle:
// reporting it and moving to the next is better than blocking the loop on it, which would
// deprive of a key everyone coming after.
func (r *KeyReconciler) issue(ctx context.Context, project store.PendingKey) {
	public, err := r.keys.Issue(ctx, project.Slug, project.Known)
	switch {
	case errors.Is(err, deploykeys.ErrNameTaken):
		r.logger.Error("deploy key not set",
			"project", project.Slug,
			"reason", "a secret already carries this name and was not created by the dashboard",
			"consequence", "the project uses the deployment's shared key")
		return
	case err != nil:
		r.logger.Error("setting the deploy key",
			"project", project.Slug, "error", err)
		return
	}

	// The database **after** the cluster. The reverse would suggest a key was set if the store
	// failed, and a later rotation would then replace a Secret that does not exist — or someone
	// else's, since the column would have asserted an ownership the dashboard does not have.
	if err := r.store.SetDeployKey(ctx, project.ID, public); err != nil {
		r.logger.Error("recording the deploy key",
			"project", project.Slug, "error", err)
		return
	}

	// Logged as an outgoing action: "who changed the key, and when" belongs to the audit just as
	// much as a promotion. The actor is the dashboard itself — the request carries the name of
	// whoever made it.
	_ = r.store.Audit(ctx, "dashboard", store.AuditProjectSet, project.Slug,
		map[string]any{"deploy_key": "set", "rotation": project.Known})

	r.logger.Info("deploy key set",
		"project", project.Slug, "rotation", project.Known)
}
