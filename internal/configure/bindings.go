package configure

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// BindingStore is what the capabilities' recorded bindings need from the database.
type BindingStore interface {
	SetCapabilityBinding(ctx context.Context, ref string, binding json.RawMessage, actor string) error
	DeleteCapabilityBinding(ctx context.Context, ref string) error
	Auditor
}

// Bindings records the bindings an administrator gives the pipeline capabilities the catalogue
// file does not bind. Never a service account: granting a cluster identity is the operator's act,
// versioned with the deployment.
type Bindings struct {
	Store    BindingStore
	Registry capabilities.Source
	Catalog  catalog.Source
	// Check refuses a binding with which the capabilities would no longer build.
	Check func(ctx context.Context, binding catalog.Binding) error
	// Reload applies a recorded binding now rather than at the next poll. Nil: the poll applies it.
	Reload func(ctx context.Context) error
	Logger *slog.Logger
}

// Ref resolves a capability the registry declares a pipeline for.
func (s Bindings) Ref(raw string) (capability.Ref, error) {
	ref, err := capability.ParseRef(raw)
	if err != nil {
		return capability.Ref{}, refuse(http.StatusNotFound, "Unknown capability", err.Error())
	}
	if _, _, found := capabilities.Of(s.Registry).Pipeline(ref); !found {
		return capability.Ref{}, refuse(http.StatusNotFound, "Unknown capability", ref.String())
	}
	return ref, nil
}

// Set records a capability's binding, after checking the capabilities still build with it.
func (s Bindings) Set(ctx context.Context, actor string, ref capability.Ref, raw []byte) (json.RawMessage, error) {
	logger := loggerOr(s.Logger)
	binding, err := catalog.ParseBinding(raw)
	if err != nil {
		return nil, invalid("Invalid binding", err)
	}
	if binding.Ref == "" {
		binding.Ref = ref.String()
	}
	if binding.Ref != ref.String() {
		return nil, refuse(http.StatusBadRequest, "Invalid binding", "the binding names another capability")
	}
	if catalog.Of(s.Catalog).BoundByFile(binding.Ref) {
		return nil, refuse(http.StatusConflict, "Bound by the catalogue file", "the file's binding wins: change it there")
	}
	if s.Check != nil {
		if err := s.Check(ctx, binding); err != nil {
			return nil, invalid("Invalid binding", err)
		}
	}
	normalised, err := json.Marshal(binding)
	if err != nil {
		return nil, failure(logger, "Cannot record the binding", err, "capability", binding.Ref)
	}
	if err := s.Store.SetCapabilityBinding(ctx, binding.Ref, normalised, actor); err != nil {
		return nil, failure(logger, "Cannot record the binding", err, "capability", binding.Ref)
	}
	audit(ctx, logger, s.Store, actor, "capability.binding", binding.Ref, nil)
	s.applyNow(ctx, logger)
	return normalised, nil
}

// Delete removes a recorded binding: the capability can no longer be launched.
func (s Bindings) Delete(ctx context.Context, actor string, ref capability.Ref) error {
	logger := loggerOr(s.Logger)
	if err := s.Store.DeleteCapabilityBinding(ctx, ref.String()); err != nil {
		return failure(logger, "Cannot remove the binding", err, "capability", ref.String())
	}
	audit(ctx, logger, s.Store, actor, "capability.unbinding", ref.String(), nil)
	s.applyNow(ctx, logger)
	return nil
}

func (s Bindings) applyNow(ctx context.Context, logger *slog.Logger) {
	if s.Reload == nil {
		return
	}
	if err := s.Reload(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("applying a binding", "error", err)
	}
}
