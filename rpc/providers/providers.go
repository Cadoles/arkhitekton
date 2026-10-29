// Package providers carries a native plugin's tracker, forge, probes and actions over the wire.
//
// The dashboard uses [Tracker], [Forge], [Probe] and [Executor]: they implement the sdk's
// capability interfaces by calling the plugin's program, so that the rest of the dashboard sees a
// native plugin's tracker as it sees a compiled one's. The plugin side is the extension package,
// which serves the same sdk interfaces.
//
// No connection lives across calls on the dashboard's side: every call carries its target, and the
// program connects for it. A provider's errors cross as themselves ([provider.ErrNotConfigured],
// [provider.ErrWriteForbidden], [provider.ErrTagExists]): the dashboard branches on them.
package providers

import (
	"errors"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"forge.cadoles.com/vcarroy/arkhitekton/rpc/pluginv1"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

// The connections Connect checks.
const (
	KindTracker = "tracker"
	KindForge   = "forge"
	KindWriter  = "writer"
	KindProber  = "prober"
)

// IDPlaceholder stands for an identifier when the program computes the tracker's links: letters
// only, so that a tracker escaping its identifiers leaves it as it is. The dashboard receives
// `{id}` in its place.
const IDPlaceholder = "arkhitektonlinkid"

// The wire forms of the calls' arguments and results, as JSON.
type (
	VersionsArguments struct{ Project string }
	RefArguments      struct{ Ref string }
	SinceArguments    struct{ Since time.Time }
	TagArguments      struct{ Tag, Commit, Message string }
	ProbeArguments    struct{ Repository, PublicKey string }
	RefsResult        struct {
		Refs      []provider.Ref
		Truncated bool
	}
)

// TargetOf returns the wire form of a target.
func TargetOf(target capability.Target) *pluginv1.ProviderTarget {
	return &pluginv1.ProviderTarget{
		Project: target.Project, Repository: target.Repository, Settings: target.Settings,
		TimeoutMs: target.Timeout.Milliseconds(),
	}
}

// TargetFrom returns the target a wire form carries.
func TargetFrom(wire *pluginv1.ProviderTarget) capability.Target {
	return capability.Target{
		Project: wire.GetProject(), Repository: wire.GetRepository(), Settings: wire.GetSettings(),
		Timeout: time.Duration(wire.GetTimeoutMs()) * time.Millisecond,
	}
}

const errorDomain = "arkhitekton.plugin.v1"

// The sentinel errors that cross the wire, by the reason that names them there.
var sentinels = []struct {
	err    error
	code   codes.Code
	reason string
}{
	{provider.ErrNotConfigured, codes.FailedPrecondition, "NOT_CONFIGURED"},
	{provider.ErrWriteForbidden, codes.PermissionDenied, "WRITE_FORBIDDEN"},
	{provider.ErrTagExists, codes.AlreadyExists, "TAG_EXISTS"},
}

// Status returns the error a plugin's program answers for a provider's error.
func Status(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	for _, sentinel := range sentinels {
		if !errors.Is(err, sentinel.err) {
			continue
		}
		state, detailed := status.New(sentinel.code, err.Error()).
			WithDetails(&errdetails.ErrorInfo{Reason: sentinel.reason, Domain: errorDomain})
		if detailed != nil {
			return status.Error(sentinel.code, err.Error())
		}
		return state.Err()
	}
	return status.Error(codes.Unknown, err.Error())
}

// remoteError is a provider's error answered by the program: its message, and the sentinel it
// stands for.
type remoteError struct {
	message  string
	sentinel error
}

func (e remoteError) Error() string { return e.message }
func (e remoteError) Unwrap() error { return e.sentinel }

// Error returns the provider's error a program's answer stands for.
func Error(err error) error {
	state, ok := status.FromError(err)
	if !ok || err == nil {
		return err
	}
	for _, detail := range state.Details() {
		info, ok := detail.(*errdetails.ErrorInfo)
		if !ok || info.GetDomain() != errorDomain {
			continue
		}
		for _, sentinel := range sentinels {
			if info.GetReason() == sentinel.reason {
				return remoteError{message: state.Message(), sentinel: sentinel.err}
			}
		}
	}
	return err
}
