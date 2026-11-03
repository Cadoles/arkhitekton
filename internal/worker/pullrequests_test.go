package worker

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

// pullForge answers its current pull requests, filtered on the cursor as a forge does.
type pullForge struct {
	pulls       []provider.PullRequest
	unsupported bool
}

func (f *pullForge) Connect(capability.Target) (provider.Forge, error) {
	if f.unsupported {
		return plainForge{}, nil
	}
	return listingForge{f}, nil
}
func (f *pullForge) Writer(capability.Target) (provider.TagWriter, error)        { return nil, nil }
func (f *pullForge) Prober(capability.Target) (provider.RepositoryProber, error) { return nil, nil }

type plainForge struct{ provider.Forge }

type listingForge struct {
	*pullForge
}

func (f listingForge) DefaultBranch(context.Context) (string, error) { return "main", nil }
func (f listingForge) ListBranches(context.Context) ([]provider.Ref, bool, error) {
	return nil, false, nil
}
func (f listingForge) ListTags(context.Context) ([]provider.Ref, bool, error) { return nil, false, nil }
func (f listingForge) ResolveCommit(context.Context, string) (string, error)  { return "", nil }
func (f listingForge) ListMergedPullRequests(context.Context, time.Time) ([]provider.PullRequest, error) {
	return nil, nil
}
func (f listingForge) LoadCommitMessages(context.Context, *provider.PullRequest) error { return nil }
func (f listingForge) CheckAccess(context.Context) (provider.Account, error) {
	return provider.Account{}, nil
}

func (f listingForge) ListPullRequests(_ context.Context, since time.Time) ([]provider.PullRequest, bool, error) {
	var changed []provider.PullRequest
	for index := len(f.pulls) - 1; index >= 0; index-- {
		if since.IsZero() || f.pulls[index].UpdatedAt.After(since) {
			changed = append(changed, f.pulls[index])
		}
	}
	return changed, false, nil
}

// pullStore keeps the requests in memory, as the database would.
type pullStore struct {
	mu    sync.Mutex
	pulls map[int64]store.PullRequest
	next  int64
}

func (s *pullStore) UpsertPullRequest(_ context.Context, pull store.PullRequest) (store.PullRequest, *store.PullRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var previous *store.PullRequest
	if existing, found := s.pulls[pull.Number]; found {
		previous = &existing
		pull.ID = existing.ID
	} else {
		s.next++
		pull.ID = s.next
	}
	s.pulls[pull.Number] = pull
	return pull, previous, nil
}

type pullScenario struct {
	t      *testing.T
	forge  *pullForge
	probe  pullRequestProbe
	cursor string
	clock  time.Time
}

func newPullScenario(t *testing.T) *pullScenario {
	forge := &pullForge{}
	scenario := &pullScenario{t: t, forge: forge, clock: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)}
	scenario.probe = pullRequestProbe{
		plugin: "gitea", forge: forge, store: &pullStore{pulls: map[int64]store.PullRequest{}},
		registry: "reg.example/team", logger: quietLogger(), now: func() time.Time { return scenario.clock },
	}
	return scenario
}

// set puts a request in the forge as it now stands, updated a minute later than anything before.
func (s *pullScenario) set(number int64, state, head string) {
	s.clock = s.clock.Add(time.Minute)
	pull := provider.PullRequest{
		Number: number, Title: "Feature", State: state, Author: "dev", SourceBranch: "feature",
		TargetBranch: "main", HeadSHA: head, URL: "https://forge.test/pulls", UpdatedAt: s.clock,
	}
	for index, existing := range s.forge.pulls {
		if existing.Number == number {
			s.forge.pulls = append(s.forge.pulls[:index], s.forge.pulls[index+1:]...)
			break
		}
	}
	s.forge.pulls = append(s.forge.pulls, pull)
}

func (s *pullScenario) pass() []event.Event {
	s.t.Helper()
	facts, cursor, err := s.probe.Poll(context.Background(), store.Project{ID: 3, Slug: "app", Repository: "team/app"}, nil, s.cursor)
	if err != nil {
		s.t.Fatalf("poll: %v", err)
	}
	s.cursor = cursor
	return facts
}

func types(facts []event.Event) string {
	names := make([]string, 0, len(facts))
	for _, fact := range facts {
		names = append(names, string(fact.Type))
	}
	return strings.Join(names, ",")
}

func TestThePullRequestsTransitionsBecomeFacts(t *testing.T) {
	s := newPullScenario(t)
	s.set(1, provider.PullOpen, "aaaaaaaaaa")
	s.set(2, provider.PullMerged, "bbbbbbbbbb")
	if facts := s.pass(); len(facts) != 0 {
		t.Fatalf("the first pass announces the history: %s", types(facts))
	}

	s.set(3, provider.PullOpen, "cccccccccc")
	facts := s.pass()
	if types(facts) != string(event.PullRequestOpened) {
		t.Fatalf("opened: %s", types(facts))
	}
	payload := facts[0].Payload
	if payload["revision"] != "cccccccccc" || payload["tag"] != "pr-3-ccccccc" || payload["registry"] != "reg.example/team" ||
		payload[ContextPullRequest] == "" || facts[0].Subject != "#3" {
		t.Errorf("payload = %v, subject %q", payload, facts[0].Subject)
	}

	s.set(1, provider.PullOpen, "dddddddddd")
	if facts := s.pass(); types(facts) != string(event.PullRequestSynchronized) || !strings.HasSuffix(facts[0].DedupKey, ":dddddddddd") {
		t.Errorf("pushed to: %s %v", types(facts), facts)
	}

	s.set(1, provider.PullClosed, "dddddddddd")
	if facts := s.pass(); types(facts) != string(event.PullRequestClosed) {
		t.Errorf("closed: %s", types(facts))
	}
	s.set(1, provider.PullOpen, "dddddddddd")
	if facts := s.pass(); types(facts) != string(event.PullRequestReopened) {
		t.Errorf("reopened: %s", types(facts))
	}
	s.set(1, provider.PullMerged, "dddddddddd")
	if facts := s.pass(); types(facts) != string(event.PullRequestMerged) {
		t.Errorf("merged: %s", types(facts))
	}

	if facts := s.pass(); len(facts) != 0 {
		t.Errorf("a pass with nothing changed announces: %s", types(facts))
	}
}

// A forge that cannot list its requests is not followed, and nothing fails.
func TestAForgeThatCannotListIsNotFollowed(t *testing.T) {
	s := newPullScenario(t)
	s.forge.unsupported = true
	s.cursor = "kept"
	facts, cursor, err := s.probe.Poll(context.Background(), store.Project{ID: 3}, nil, s.cursor)
	if err != nil || len(facts) != 0 || cursor != "kept" {
		t.Errorf("facts %v, cursor %q, %v", facts, cursor, err)
	}
}

// probeFake records which projects a probe was polled for.
type probeFake struct {
	mu     sync.Mutex
	polled []int64
}

func (p *probeFake) PluginID() string   { return "gitea" }
func (p *probeFake) Source() string     { return "gitea.pull_request_sync" }
func (p *probeFake) Schedule() Schedule { return Schedule{Interval: time.Hour} }
func (p *probeFake) Poll(_ context.Context, project store.Project, _ map[string]string, cursor string) ([]event.Event, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.polled = append(p.polled, project.ID)
	return nil, cursor, nil
}

// requestStore answers the projects and a single early pass.
type requestStore struct {
	ProbeStore
	requests []store.SyncRequest
}

func (s *requestStore) ListProjects(context.Context) ([]store.Project, error) {
	return []store.Project{{ID: 1, Slug: "one"}, {ID: 2, Slug: "two"}}, nil
}
func (s *requestStore) ResolvedPlugin(context.Context, int64, string) (store.PluginSettings, error) {
	return store.PluginSettings{}, nil
}
func (s *requestStore) GetSyncState(_ context.Context, source string, _ int64) (store.SyncState, error) {
	return store.SyncState{Source: source, Cursor: "c"}, nil
}
func (s *requestStore) RecordSyncSuccess(context.Context, string, int64, string) error { return nil }
func (s *requestStore) TakeSyncRequests(context.Context) ([]store.SyncRequest, error) {
	taken := s.requests
	s.requests = nil
	return taken, nil
}

// A pass asked for early runs the source's probe over that project only.
func TestAnEarlyPassPollsTheProjectAskedFor(t *testing.T) {
	probe := &probeFake{}
	db := &requestStore{requests: []store.SyncRequest{{Source: probe.Source(), ProjectID: 2}, {Source: "other", ProjectID: 1}}}
	registry, err := capabilities.New(capability.Plugin{ID: "gitea", Version: "1.0.0", APIVersion: sdk.APIVersion})
	if err != nil {
		t.Fatal(err)
	}
	runner := NewProbeRunner(registry, db, nil, time.Hour, quietLogger(), probe)
	loop := &probeLoop{probe: probe}

	runner.serveRequests(context.Background(), map[string]*probeLoop{probe.Source(): loop})
	deadline := time.Now().Add(time.Second)
	for {
		probe.mu.Lock()
		polled := append([]int64(nil), probe.polled...)
		probe.mu.Unlock()
		if len(polled) > 0 || time.Now().After(deadline) {
			if len(polled) != 1 || polled[0] != 2 {
				t.Errorf("polled = %v, want project 2 only", polled)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A draft carries `draft: true`; leaving the draft is a fact of its own. The body's `Key: value`
// lines become trailers a step reads.
func TestADraftLeavesItsDraftAsAFact(t *testing.T) {
	s := newPullScenario(t)
	s.set(1, provider.PullOpen, "aaaaaaaaaa")
	s.pass()

	s.set(4, provider.PullOpen, "eeeeeeeeee")
	s.forge.pulls[len(s.forge.pulls)-1].Draft = true
	s.forge.pulls[len(s.forge.pulls)-1].Body = "Correctif.\n\nmse-kustom: feature/branche\nRefs: #12"
	facts := s.pass()
	if types(facts) != string(event.PullRequestOpened) || facts[0].Payload["draft"] != "true" {
		t.Fatalf("opened as a draft: %s %v", types(facts), facts)
	}
	if facts[0].Payload["trailer.mse-kustom"] != "feature/branche" || facts[0].Payload["trailer.refs"] != "#12" {
		t.Errorf("trailers %v", facts[0].Payload)
	}

	s.set(4, provider.PullOpen, "eeeeeeeeee")
	facts = s.pass()
	if types(facts) != string(event.PullRequestReady) || facts[0].Payload["draft"] != "false" {
		t.Errorf("out of draft: %s %v", types(facts), facts)
	}
}

func TestTrailersAreTheBodysKeyValueLines(t *testing.T) {
	got := Trailers("Titre\n\nMSE-Kustom:  release/3.2 \nmse-kustom: autre\nune phrase: avec deux points\n:vide\n")
	if got["mse-kustom"] != "release/3.2" || len(got) != 1 {
		t.Errorf("trailers %v", got)
	}
}
