package extension

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"forge.cadoles.com/vcarroy/arkhitekton/rpc/pluginv1"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/providers"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

// connectionsKept bounds the connections kept, one per kind and distinct target.
const connectionsKept = 64

// connections keeps the plugin's connections by kind and target: the dashboard sends the target
// with every call, and connecting for each one would rebuild a client per row read.
type connections struct {
	mu   sync.Mutex
	kept map[string]any
}

func (c *connections) get(kind string, target capability.Target, open func() (any, error)) (any, error) {
	key := kind + ":" + fingerprint(target)
	c.mu.Lock()
	known, ok := c.kept[key]
	c.mu.Unlock()
	if ok {
		return known, nil
	}
	opened, err := open()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.kept == nil || len(c.kept) >= connectionsKept {
		c.kept = make(map[string]any)
	}
	c.kept[key] = opened
	return opened, nil
}

func fingerprint(target capability.Target) string {
	keys := make([]string, 0, len(target.Settings))
	for key := range target.Settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%q;%q;%d;", target.Project, target.Repository, target.Timeout)
	for _, key := range keys {
		_, _ = fmt.Fprintf(hash, "%q=%q;", key, target.Settings[key])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// guarded runs a provider's code, a panic answered as an error rather than bringing the plugin down.
func guarded(run func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("the plugin panicked: %v", recovered)
		}
	}()
	return providers.Status(run())
}

func (s *server) tracker() (capability.TrackerCapability, error) {
	hooks, err := s.started()
	if err != nil {
		return nil, err
	}
	if hooks.Tracker == nil {
		return nil, unimplemented("tracker")
	}
	return hooks.Tracker, nil
}

func (s *server) forge() (capability.ForgeCapability, error) {
	hooks, err := s.started()
	if err != nil {
		return nil, err
	}
	if hooks.Forge == nil {
		return nil, unimplemented("forge")
	}
	return hooks.Forge, nil
}

// open returns the connection of a kind for a target. Nil, without error, for a writer or a prober
// the forge does not have.
func (s *server) open(kind string, target capability.Target) (any, error) {
	switch kind {
	case providers.KindTracker:
		tracker, err := s.tracker()
		if err != nil {
			return nil, err
		}
		return s.connections.get(kind, target, func() (any, error) { return tracker.Connect(target) })
	case providers.KindForge, providers.KindWriter, providers.KindProber:
		forge, err := s.forge()
		if err != nil {
			return nil, err
		}
		return s.connections.get(kind, target, func() (any, error) {
			switch kind {
			case providers.KindWriter:
				writer, err := forge.Writer(target)
				if writer == nil {
					return nil, err
				}
				return writer, err
			case providers.KindProber:
				prober, err := forge.Prober(target)
				if prober == nil {
					return nil, err
				}
				return prober, err
			default:
				return forge.Connect(target)
			}
		})
	default:
		return nil, status.Errorf(codes.InvalidArgument, "no connection of kind %q", kind)
	}
}

func (s *server) Connect(_ context.Context, request *pluginv1.ConnectRequest) (*pluginv1.ConnectResponse, error) {
	var opened any
	err := guarded(func() error {
		var err error
		opened, err = s.open(request.GetKind(), providers.TargetFrom(request.GetTarget()))
		return err
	})
	if err != nil {
		return nil, err
	}
	return &pluginv1.ConnectResponse{Absent: opened == nil}, nil
}

func (s *server) TrackerLinks(_ context.Context, request *pluginv1.Settings) (*pluginv1.Links, error) {
	tracker, err := s.tracker()
	if err != nil {
		return nil, err
	}
	links := &pluginv1.Links{}
	err = guarded(func() error {
		settings := capability.Settings(request.GetValues())
		links.Version = strings.ReplaceAll(tracker.VersionURL(settings, providers.IDPlaceholder), providers.IDPlaceholder, "{id}")
		links.Issue = strings.ReplaceAll(tracker.IssueURL(settings, providers.IDPlaceholder), providers.IDPlaceholder, "{id}")
		return nil
	})
	if err != nil {
		return nil, err
	}
	return links, nil
}

// answer runs a provider call on the connection of a kind: its arguments decoded, its result
// encoded.
func answer[C, A any](
	ctx context.Context, s *server, kind string, request *pluginv1.ProviderRequest,
	run func(context.Context, C, A) (any, error),
) (*pluginv1.ProviderResponse, error) {
	var arguments A
	if err := json.Unmarshal(request.GetArguments(), &arguments); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "arguments: %v", err)
	}
	var encoded []byte
	err := guarded(func() error {
		opened, err := s.open(kind, providers.TargetFrom(request.GetTarget()))
		if err != nil {
			return err
		}
		connection, ok := opened.(C)
		if !ok {
			return status.Errorf(codes.FailedPrecondition, "the plugin has no %s for this target", kind)
		}
		result, err := run(ctx, connection, arguments)
		if err != nil {
			return err
		}
		encoded, err = json.Marshal(result)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &pluginv1.ProviderResponse{Result: encoded}, nil
}

type none struct{}

func (s *server) ListVersions(ctx context.Context, request *pluginv1.ProviderRequest) (*pluginv1.ProviderResponse, error) {
	return answer(ctx, s, providers.KindTracker, request,
		func(ctx context.Context, tracker provider.Tracker, arguments providers.VersionsArguments) (any, error) {
			return tracker.ListVersions(ctx, arguments.Project)
		})
}

func (s *server) ListIssues(ctx context.Context, request *pluginv1.ProviderRequest) (*pluginv1.ProviderResponse, error) {
	return answer(ctx, s, providers.KindTracker, request,
		func(ctx context.Context, tracker provider.Tracker, query provider.IssueQuery) (any, error) {
			return tracker.ListIssues(ctx, query)
		})
}

func (s *server) TrackerAccess(ctx context.Context, request *pluginv1.ProviderRequest) (*pluginv1.ProviderResponse, error) {
	return answer(ctx, s, providers.KindTracker, request,
		func(ctx context.Context, tracker provider.Tracker, _ none) (any, error) {
			return tracker.CheckAccess(ctx)
		})
}

func (s *server) DefaultBranch(ctx context.Context, request *pluginv1.ProviderRequest) (*pluginv1.ProviderResponse, error) {
	return answer(ctx, s, providers.KindForge, request,
		func(ctx context.Context, forge provider.Forge, _ none) (any, error) {
			return forge.DefaultBranch(ctx)
		})
}

func (s *server) ListBranches(ctx context.Context, request *pluginv1.ProviderRequest) (*pluginv1.ProviderResponse, error) {
	return answer(ctx, s, providers.KindForge, request,
		func(ctx context.Context, forge provider.Forge, _ none) (any, error) {
			refs, truncated, err := forge.ListBranches(ctx)
			return providers.RefsResult{Refs: refs, Truncated: truncated}, err
		})
}

func (s *server) ListTags(ctx context.Context, request *pluginv1.ProviderRequest) (*pluginv1.ProviderResponse, error) {
	return answer(ctx, s, providers.KindForge, request,
		func(ctx context.Context, forge provider.Forge, _ none) (any, error) {
			refs, truncated, err := forge.ListTags(ctx)
			return providers.RefsResult{Refs: refs, Truncated: truncated}, err
		})
}

func (s *server) ResolveCommit(ctx context.Context, request *pluginv1.ProviderRequest) (*pluginv1.ProviderResponse, error) {
	return answer(ctx, s, providers.KindForge, request,
		func(ctx context.Context, forge provider.Forge, arguments providers.RefArguments) (any, error) {
			return forge.ResolveCommit(ctx, arguments.Ref)
		})
}

func (s *server) ListMergedPullRequests(ctx context.Context, request *pluginv1.ProviderRequest) (*pluginv1.ProviderResponse, error) {
	return answer(ctx, s, providers.KindForge, request,
		func(ctx context.Context, forge provider.Forge, arguments providers.SinceArguments) (any, error) {
			return forge.ListMergedPullRequests(ctx, arguments.Since)
		})
}

func (s *server) LoadCommitMessages(ctx context.Context, request *pluginv1.ProviderRequest) (*pluginv1.ProviderResponse, error) {
	return answer(ctx, s, providers.KindForge, request,
		func(ctx context.Context, forge provider.Forge, pull provider.PullRequest) (any, error) {
			err := forge.LoadCommitMessages(ctx, &pull)
			return pull, err
		})
}

func (s *server) ForgeAccess(ctx context.Context, request *pluginv1.ProviderRequest) (*pluginv1.ProviderResponse, error) {
	return answer(ctx, s, providers.KindForge, request,
		func(ctx context.Context, forge provider.Forge, _ none) (any, error) {
			return forge.CheckAccess(ctx)
		})
}

func (s *server) CreateTag(ctx context.Context, request *pluginv1.ProviderRequest) (*pluginv1.ProviderResponse, error) {
	return answer(ctx, s, providers.KindWriter, request,
		func(ctx context.Context, writer provider.TagWriter, arguments providers.TagArguments) (any, error) {
			return nil, writer.CreateTag(ctx, arguments.Tag, arguments.Commit, arguments.Message)
		})
}

func (s *server) ProbeRepository(ctx context.Context, request *pluginv1.ProviderRequest) (*pluginv1.ProviderResponse, error) {
	return answer(ctx, s, providers.KindProber, request,
		func(ctx context.Context, prober provider.RepositoryProber, arguments providers.ProbeArguments) (any, error) {
			return prober.Probe(ctx, arguments.Repository, arguments.PublicKey)
		})
}

func (s *server) Poll(ctx context.Context, request *pluginv1.PollRequest) (*pluginv1.PollResponse, error) {
	hooks, err := s.started()
	if err != nil {
		return nil, err
	}
	var probe capability.Probe
	for _, candidate := range hooks.Probes {
		if candidate.Name() == request.GetProbe() {
			probe = candidate
		}
	}
	if probe == nil {
		return nil, unimplemented("probe " + request.GetProbe())
	}
	response := &pluginv1.PollResponse{}
	err = guarded(func() error {
		observations, cursor, err := probe.Poll(ctx, providers.TargetFrom(request.GetTarget()), request.GetCursor())
		if err != nil {
			return err
		}
		response.Cursor = cursor
		response.Observations, err = json.Marshal(observations)
		return err
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

func (s *server) Execute(ctx context.Context, request *pluginv1.ExecuteRequest) (*pluginv1.Empty, error) {
	hooks, err := s.started()
	if err != nil {
		return nil, err
	}
	if hooks.Executor == nil {
		return nil, unimplemented("actions")
	}
	err = guarded(func() error {
		return hooks.Executor.Execute(ctx, providers.TargetFrom(request.GetTarget()), request.GetSuffix(), request.GetParams())
	})
	if err != nil {
		return nil, err
	}
	return &pluginv1.Empty{}, nil
}

func (s *server) Receive(ctx context.Context, request *pluginv1.ReceiveRequest) (*pluginv1.ReceiveResponse, error) {
	hooks, err := s.started()
	if err != nil {
		return nil, err
	}
	if hooks.Receiver == nil {
		return nil, unimplemented("webhook")
	}
	response := &pluginv1.ReceiveResponse{}
	err = guarded(func() error {
		observations, err := hooks.Receiver.Receive(ctx, providers.TargetFrom(request.GetTarget()),
			capability.Delivery{Event: request.GetEvent(), Body: request.GetBody()})
		if err != nil {
			return err
		}
		response.Observations, err = json.Marshal(observations)
		return err
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}
