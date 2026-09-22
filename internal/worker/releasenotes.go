package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/provider"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/release"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Detecting the requests a release brings.
 *
 * Two conditions, and both count:
 *
 *   * the request is **in review** in the tracker — its code is written and reviewed;
 *   * a **merged** pull request cites it, in its title or in one of its commit messages.
 *
 * Neither is enough on its own. A request in review with nothing merged is not in the image; a
 * merged PR citing a request still in development names partial work. Announcing either to the
 * client would be a false note, and a false note is worse than an empty one: it reads as a
 * checked list.
 *
 * The detection runs **when the release is created** and does not replay itself. What it found
 * becomes a proposal one completes or corrects by hand: replaying would erase those
 * adjustments.
 */

// PullReader is what the detection needs from the forge.
type PullReader interface {
	ListMergedPullRequests(ctx context.Context, since time.Time) ([]provider.PullRequest, error)
	LoadCommitMessages(ctx context.Context, pull *provider.PullRequest) error
}

// NoteStore is what the detection needs from the database.
type NoteStore interface {
	NoteCandidates(ctx context.Context, releaseID int64, statuses []string) (map[string]store.NoteEntry, error)
	LinkNoteIssue(ctx context.Context, releaseID int64, reference, source, pullRequest, actor string) error
}

// NoteBuilder composes a release's note when it is created.
type NoteBuilder struct {
	forge  PullReader
	store  NoteStore
	logger *slog.Logger
	// statuses lists the tracker statuses that make a request eligible. Configurable: the
	// vocabulary belongs to the client, who may rename "In review" without this code having to
	// change.
	statuses []string
}

func NewNoteBuilder(
	forge PullReader, notes NoteStore, statuses []string, logger *slog.Logger,
) *NoteBuilder {
	return &NoteBuilder{forge: forge, store: notes, statuses: statuses, logger: logger}
}

// NoteResult says what the detection found.
type NoteResult struct {
	// Linked is the number of requests attached.
	Linked int
	// Pulls is the number of merged pull requests examined.
	Pulls int
	// Incomplete reports that at least one PR could not be read whole. The note is then a partial
	// proposal, and saying so avoids taking it for exhaustive.
	Incomplete bool
}

// Build attaches the detected requests to the release.
//
// `since` bounds the reading of the PRs: the previous release's creation date, when there is
// one. Without it, the detection goes back to the forge client's own bound.
func (b *NoteBuilder) Build(
	ctx context.Context, releaseID int64, since time.Time, actor string,
) (NoteResult, error) {
	var result NoteResult

	candidates, err := b.store.NoteCandidates(ctx, releaseID, b.statuses)
	if err != nil {
		return result, err
	}
	// No request in review: nothing can be detected, and querying the forge to find that out would
	// cost two hundred requests for nothing.
	if len(candidates) == 0 {
		return result, nil
	}

	pulls, err := b.forge.ListMergedPullRequests(ctx, since)
	if err != nil {
		return result, err
	}
	result.Pulls = len(pulls)

	for index := range pulls {
		pull := &pulls[index]

		// The commits complete the title: the convention allows the number in either. A read failure
		// does not fail the detection — the title stays usable — but it is reported, the note becoming
		// partial.
		if err := b.forge.LoadCommitMessages(ctx, pull); err != nil {
			result.Incomplete = true
			b.logger.Warn("commits of a pull request unreadable",
				"pull", pull.Number, "error", err)
		}

		texts := append([]string{pull.Title, pull.Body}, pull.Commits...)
		for _, reference := range release.ExtractIssueRefs(texts...) {
			if _, eligible := candidates[reference]; !eligible {
				continue
			}
			err := b.store.LinkNoteIssue(ctx, releaseID, reference,
				store.NoteSourceAuto, pull.Reference(), actor)
			switch {
			case errors.Is(err, store.ErrNoteAlreadyLinked):
				// Two PRs for one request: it is common — a fix then its follow-up — and the first attached is
				// authoritative.
				continue
			case err != nil:
				return result, err
			}
			result.Linked++
		}
	}

	b.logger.Info("release note composed",
		"release", releaseID, "requests", result.Linked,
		"pull_requests", result.Pulls, "partielle", result.Incomplete)
	return result, nil
}

// KindReleaseNotes is the kind of job that composes a release note.
//
// Absent from the catalogue: this work reads the tracker, the forge and the database, and has
// no reason to cross Tekton. It goes through the queue anyway — like the exports — for its
// scheduling and its trace: composing queries up to two hundred pull requests, and doing it in
// the creation request would have left the user waiting in front of a form.
const KindReleaseNotes = "release-notes"

// Kind implements LocalTask.
func (b *NoteBuilder) Kind() string { return KindReleaseNotes }

// Run composes the job's note.
//
// The parameters carry the release and the reading bound. A failure is returned to the
// scheduler, which concludes the job: a missing note is visible on screen, and re-running it
// is a deliberate act, not a silent retry.
func (b *NoteBuilder) Run(ctx context.Context, job store.Job) error {
	if job.ReleaseID == nil {
		return errors.New("release note: no release identifier")
	}

	var since time.Time
	if raw := job.Params["since"]; raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			// An unreadable bound does not prevent composing: without it the reading goes back further,
			// which is more expensive but not wrong.
			b.logger.Warn("reading bound unreadable", "valeur", raw, "error", err)
		} else {
			since = parsed
		}
	}

	_, err := b.Build(ctx, *job.ReleaseID, since, job.RequestedBy)
	return err
}
