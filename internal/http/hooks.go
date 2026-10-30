package http

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

/*
 * What a source pushes: the one public route that changes something.
 *
 * Nobody holds a session here, and the route is reached from outside by design. What stands in
 * for authentication is the delivery's signature, keyed with a secret the project's plugin
 * settings hold, and it is checked here, before the plugin reads a byte: a check left to each
 * plugin is a check one plugin would forget.
 *
 * A delivery launches nothing by itself. It records facts, the same a probe would have recorded,
 * and the reactions a project declared do the rest. Which is also what bounds a replay: a fact is
 * identified by what it says, not by the delivery that carried it, and a fact already known is
 * neither recorded nor routed again.
 *
 * Every refusal says why. The sender is a forge an administrator configured, and its delivery
 * history is where they will look first; a project's slug and whether it receives are no secret
 * worth a blank answer.
 */

// maxDeliveryBytes bounds a delivery's body. A push of a few hundred commits stays well below; the
// bound is what keeps an unauthenticated caller from having the API read an unbounded body before
// the signature can be checked.
const maxDeliveryBytes = 4 << 20

// HooksStore is what receiving a delivery needs from the database.
type HooksStore interface {
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	ResolvedPlugin(ctx context.Context, projectID int64, pluginID string) (store.PluginSettings, error)
	RecordReceivedEvent(ctx context.Context, fact event.Event) (bool, error)
}

// HooksHandler receives the deliveries plugins declare.
type HooksHandler struct {
	registry capabilities.Source
	store    HooksStore
	// unseal opens a plugin's sensitive settings, the webhook's secret among them.
	unseal  func(pluginID string, sealed map[string]string) (map[string]string, error)
	timeout time.Duration
	logger  *slog.Logger
}

func NewHooksHandler(
	registry capabilities.Source, hooks HooksStore,
	unseal func(pluginID string, sealed map[string]string) (map[string]string, error),
	timeout time.Duration, logger *slog.Logger,
) *HooksHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &HooksHandler{registry: registry, store: hooks, unseal: unseal, timeout: timeout, logger: logger}
}

// Routes mounts the route, outside the session and CSRF checks: a forge holds neither.
func (h *HooksHandler) Routes(mux *http.ServeMux, _ *Middleware) {
	mux.HandleFunc("POST /hooks/{plugin}/projects/{project}", h.Receive)
}

type receivedVw struct {
	Facts int `json:"facts"`
	New   int `json:"new"`
}

// Receive authenticates a delivery and records the facts it carries.
func (h *HooksHandler) Receive(w http.ResponseWriter, r *http.Request) {
	plugin, err := capabilities.Of(h.registry).Plugin(r.PathValue("plugin"))
	if err != nil || plugin.Webhook == nil {
		writeProblem(w, http.StatusNotFound, "No such webhook", "")
		return
	}
	hook := plugin.Webhook

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxDeliveryBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeProblem(w, http.StatusRequestEntityTooLarge, "Delivery too large", "")
			return
		}
		writeProblem(w, http.StatusBadRequest, "Delivery unreadable", "")
		return
	}

	project, err := h.store.ProjectBySlug(r.Context(), r.PathValue("project"))
	if err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeProblem(w, http.StatusNotFound, "No such webhook", "")
			return
		}
		h.logger.Error("project of a delivery", "plugin", plugin.ID, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Cannot receive the delivery", "")
		return
	}

	settings, err := h.store.ResolvedPlugin(r.Context(), project.ID, plugin.ID)
	if err != nil {
		if errors.Is(err, store.ErrPluginDisabled) {
			writeProblem(w, http.StatusNotFound, "No such webhook",
				"plugin "+plugin.ID+" is switched off for this project")
			return
		}
		h.logger.Error("settings of a delivery", "plugin", plugin.ID, "project", project.Slug, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Cannot receive the delivery", "")
		return
	}
	config, err := h.unseal(plugin.ID, settings.Config)
	if err != nil {
		h.logger.Error("unsealing a webhook secret", "plugin", plugin.ID, "project", project.Slug, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Cannot receive the delivery", "")
		return
	}

	secret := config[hook.Secret]
	if strings.TrimSpace(secret) == "" {
		// Refused rather than accepted unsigned: a webhook nobody set a secret for is one anybody
		// could call.
		writeProblem(w, http.StatusForbidden, "No webhook secret",
			"set the secret in the settings of plugin "+plugin.ID+" for this project")
		return
	}
	if !authentic(hook, secret, r.Header, body) {
		h.logger.Warn("delivery refused", "plugin", plugin.ID, "project", project.Slug,
			"reason", "signature does not match")
		writeProblem(w, http.StatusUnauthorized, "Signature does not match", "")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	observed, err := hook.Receiver.Receive(ctx, capability.Target{
		Project: project.Slug, Repository: project.Repository,
		Settings: capability.Settings(config), Timeout: h.timeout,
	}, capability.Delivery{Event: r.Header.Get(hook.Event), Body: body})
	if err != nil {
		// The sender is authenticated: what the plugin found wrong is for it to read.
		writeProblem(w, http.StatusUnprocessableEntity, "Delivery not read", err.Error())
		return
	}
	facts, err := capabilities.Facts(plugin, observed)
	if err != nil {
		h.logger.Error("facts of a delivery", "plugin", plugin.ID, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Cannot receive the delivery", "")
		return
	}

	recorded := 0
	for _, fact := range facts {
		fact.ProjectID = &project.ID
		fresh, err := h.store.RecordReceivedEvent(r.Context(), fact)
		if err != nil {
			// The sender retries a failed delivery, and what was already recorded is not recorded twice.
			h.logger.Error("recording a received fact", "type", fact.Type, "error", err)
			writeProblem(w, http.StatusInternalServerError, "Cannot receive the delivery", "")
			return
		}
		if fresh {
			recorded++
		}
	}
	if len(facts) > 0 {
		h.logger.Info("delivery received", "plugin", plugin.ID, "project", project.Slug,
			"facts", len(facts), "new", recorded)
	}
	writeJSON(w, http.StatusAccepted, receivedVw{Facts: len(facts), New: recorded})
}

// authentic says whether a delivery carries the proof its webhook declares, compared in constant
// time.
func authentic(hook *capability.Webhook, secret string, header http.Header, body []byte) bool {
	given := strings.TrimSpace(header.Get(hook.Signature))
	if hook.Prefix != "" {
		var found bool
		if given, found = strings.CutPrefix(given, hook.Prefix); !found {
			return false
		}
	}
	if given == "" {
		return false
	}
	switch hook.Scheme {
	case capability.SignatureHMACSHA256:
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		expected := hex.EncodeToString(mac.Sum(nil))
		return hmac.Equal([]byte(strings.ToLower(given)), []byte(expected))
	case capability.SignatureToken:
		return subtle.ConstantTimeCompare([]byte(given), []byte(secret)) == 1
	default:
		return false
	}
}
