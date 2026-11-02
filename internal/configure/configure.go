// Package configure holds the rules an administrator's configuration goes through before it is
// written: defaults, references, sealing, the checks that span several entities.
//
// They lived in the HTTP handlers, which was right while the screen was the only writer. A
// configuration applied from versioned files is a second one, and writing through the store
// would skip every rule the screen enforces. Both now go through these services: the handlers
// translate a [Problem] into the response they always gave, the command prints it.
package configure

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
)

// Problem is a refusal the caller can show as it is: a status, a title, a detail.
//
// The status is the HTTP one because the screen is the writer whose messages were settled first;
// the command shows the title and the detail and ignores it.
type Problem struct {
	Status int
	Title  string
	Detail string
}

func (p *Problem) Error() string {
	if p.Detail == "" {
		return p.Title
	}
	return p.Title + ": " + p.Detail
}

func refuse(status int, title, detail string) error {
	return &Problem{Status: status, Title: title, Detail: detail}
}

func invalid(title string, err error) error {
	return refuse(http.StatusBadRequest, title, err.Error())
}

// failure reports a fault the caller cannot correct, logged here with its cause so that the
// response stays free of internals.
func failure(logger *slog.Logger, title string, err error, attrs ...any) error {
	logger.Error(title, append(attrs, "error", err)...)
	return refuse(http.StatusInternalServerError, title, "")
}

// AsProblem returns the refusal err carries, or a generic fault for an error that is not one.
func AsProblem(err error) *Problem {
	var problem *Problem
	if errors.As(err, &problem) {
		return problem
	}
	return &Problem{Status: http.StatusInternalServerError, Title: "Internal error"}
}

// Services gathers every writer, for a caller that writes a whole configuration.
type Services struct {
	Projects         Projects
	Plugins          Plugins
	ProjectPipelines ProjectPipelines
	QueueLimits      QueueLimits
	Bindings         Bindings
	StatusMappings   StatusMappings
	Automations      Automations
}

// Auditor records an administration act.
type Auditor interface {
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// audit records an act. Without an actor nothing is recorded, as before the extraction: an
// unauthenticated caller never reaches a write, and a test calling a service bare needs no log.
func audit(ctx context.Context, logger *slog.Logger, store Auditor, actor, action, target string, payload map[string]any) {
	if actor == "" {
		return
	}
	if err := store.Audit(ctx, actor, action, target, payload); err != nil {
		logger.Error("audit log", "action", action, "target", target, "error", err)
	}
}

func loggerOr(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}
	return logger
}
