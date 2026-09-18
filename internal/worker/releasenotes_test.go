package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	dashhttp "forge.cadoles.com/vcarroy/arkhitekton/internal/http"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/provider"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

type fakeForge struct {
	pulls      []provider.PullRequest
	commits    map[int64][]string
	commitsErr error
	since      time.Time
}

func (f *fakeForge) ListMergedPullRequests(_ context.Context, since time.Time) ([]provider.PullRequest, error) {
	f.since = since
	return f.pulls, nil
}

func (f *fakeForge) LoadCommitMessages(_ context.Context, pull *provider.PullRequest) error {
	if f.commitsErr != nil {
		return f.commitsErr
	}
	pull.Commits = f.commits[pull.Number]
	return nil
}

type fakeNoteStore struct {
	candidates map[string]store.NoteEntry
	linked     []string
	sources    map[string]string
}

func (f *fakeNoteStore) NoteCandidates(context.Context, int64, []string) (map[string]store.NoteEntry, error) {
	return f.candidates, nil
}

func (f *fakeNoteStore) LinkNoteIssue(
	_ context.Context, _ int64, reference, source, pullRequest, _ string,
) error {
	for _, already := range f.linked {
		if already == reference {
			return store.ErrNoteAlreadyLinked
		}
	}
	f.linked = append(f.linked, reference)
	if f.sources == nil {
		f.sources = map[string]string{}
	}
	f.sources[reference] = source + " " + pullRequest
	return nil
}

func candidates(ids ...string) map[string]store.NoteEntry {
	entries := map[string]store.NoteEntry{}
	for _, id := range ids {
		entries[id] = store.NoteEntry{ExternalID: id, Known: true}
	}
	return entries
}

// Both conditions are necessary, and that is the whole subject: a request in review with
// nothing merged is not in the image, and a merged PR citing a request still in development
// names partial work.
func TestOnlyRequestsInReviewCitedByAMergedPRAreKept(t *testing.T) {
	forge := &fakeForge{
		pulls: []provider.PullRequest{
			{Number: 10, Title: "Fix pagination (#1234)"},
			// Cites a request that is not in review: ignored.
			{Number: 11, Title: "wip on #9999"},
		},
	}
	notes := &fakeNoteStore{candidates: candidates("1234", "4321")}

	result, err := NewNoteBuilder(forge, notes, []string{"En revue"}, quietLogger()).
		Build(context.Background(), 7, time.Time{}, "vcarroy")
	if err != nil {
		t.Fatalf("detection: %v", err)
	}

	if len(notes.linked) != 1 || notes.linked[0] != "1234" {
		t.Errorf("requests attached: %v — only 1234 meets both conditions", notes.linked)
	}
	// 4321 is in review but no PR cites it: it is not in the image.
	if result.Linked != 1 {
		t.Errorf("%d rattachements", result.Linked)
	}
}

// The number may be in a commit only: the title alone would miss the request.
func TestARequestCitedOnlyInACommitIsKept(t *testing.T) {
	forge := &fakeForge{
		pulls:   []provider.PullRequest{{Number: 12, Title: "Fix pagination"}},
		commits: map[int64][]string{12: {"wip", "fix: borne l'offset\n\nRefs #1234"}},
	}
	notes := &fakeNoteStore{candidates: candidates("1234")}

	if _, err := NewNoteBuilder(forge, notes, []string{"En revue"}, quietLogger()).
		Build(context.Background(), 7, time.Time{}, "vcarroy"); err != nil {
		t.Fatalf("detection: %v", err)
	}
	if len(notes.linked) != 1 || notes.linked[0] != "1234" {
		t.Errorf("requests attached: %v", notes.linked)
	}
}

// Two PRs for one request — a fix then its follow-up — count it only once.
func TestTwoPullRequestsForOneRequestCountItOnce(t *testing.T) {
	forge := &fakeForge{
		pulls: []provider.PullRequest{
			{Number: 20, Title: "Corrige #1234"},
			{Number: 21, Title: "Follow-up to #1234"},
		},
	}
	notes := &fakeNoteStore{candidates: candidates("1234")}

	result, err := NewNoteBuilder(forge, notes, []string{"En revue"}, quietLogger()).
		Build(context.Background(), 7, time.Time{}, "vcarroy")
	if err != nil {
		t.Fatalf("detection: %v", err)
	}
	if result.Linked != 1 || len(notes.linked) != 1 {
		t.Errorf("%d attachments for a request cited twice", result.Linked)
	}
	// The first PR met is authoritative.
	if notes.sources["1234"] != "auto #20" {
		t.Errorf("source retenue: %q", notes.sources["1234"])
	}
}

// Unreadable commits do not fail the detection — the title stays usable — but the note becomes
// partial, and saying so avoids taking it for exhaustive.
func TestUnreadableCommitsMakeTheNotePartialWithoutLosingIt(t *testing.T) {
	forge := &fakeForge{
		pulls:      []provider.PullRequest{{Number: 30, Title: "Corrige #1234"}},
		commitsErr: errors.New("forge injoignable"),
	}
	notes := &fakeNoteStore{candidates: candidates("1234")}

	result, err := NewNoteBuilder(forge, notes, []string{"En revue"}, quietLogger()).
		Build(context.Background(), 7, time.Time{}, "vcarroy")
	if err != nil {
		t.Fatalf("the detection must not fail: %v", err)
	}
	if !result.Incomplete {
		t.Error("the note should be reported partial")
	}
	if result.Linked != 1 {
		t.Errorf("%d attachments — the title stays usable", result.Linked)
	}
}

// No request in review: the forge is not queried. Two hundred requests to discover there is
// nothing to attach would be paid for at every creation.
func TestWithNoRequestInReviewTheForgeIsNotQueried(t *testing.T) {
	forge := &fakeForge{pulls: []provider.PullRequest{{Number: 40, Title: "Corrige #1234"}}}
	notes := &fakeNoteStore{candidates: map[string]store.NoteEntry{}}

	result, err := NewNoteBuilder(forge, notes, []string{"En revue"}, quietLogger()).
		Build(context.Background(), 7, time.Time{}, "vcarroy")
	if err != nil {
		t.Fatalf("detection: %v", err)
	}
	if result.Pulls != 0 {
		t.Errorf("%d PRs examined although no request is eligible", result.Pulls)
	}
}

// The reading bound is passed to the forge: without it, the detection would bring back requests
// delivered two years ago.
func TestTheReadingBoundIsPassedToTheForge(t *testing.T) {
	forge := &fakeForge{}
	notes := &fakeNoteStore{candidates: candidates("1")}
	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	if _, err := NewNoteBuilder(forge, notes, []string{"En revue"}, quietLogger()).
		Build(context.Background(), 7, since, "vcarroy"); err != nil {
		t.Fatalf("detection: %v", err)
	}
	if !forge.since.Equal(since) {
		t.Errorf("bound passed %v, expected %v", forge.since, since)
	}
}

// The `kind` is written twice — here and in the API that queues the job — to avoid an import
// cycle. Two diverging values would give a job the queue accepts and nothing processes: it
// would stay `queued` indefinitely, with no error anywhere.
func TestTheNoteKindIsTheSameOnBothSides(t *testing.T) {
	if KindReleaseNotes != dashhttp.ReleaseNotesJobKind {
		t.Errorf("worker: %q, api: %q — a queued job would never be processed",
			KindReleaseNotes, dashhttp.ReleaseNotesJobKind)
	}
}
