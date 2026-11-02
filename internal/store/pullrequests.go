package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrPullRequestNotFound reports a pull request the project does not have.
var ErrPullRequestNotFound = errors.New("pull request not found")

// PullRequest is a project's pull request, as last read from its forge.
type PullRequest struct {
	ID           int64
	ProjectID    int64
	Number       int64
	Title        string
	Author       string
	SourceBranch string
	TargetBranch string
	HeadSHA      string
	// State is `open`, `closed` or `merged`.
	State     string
	URL       string
	CreatedAt *time.Time
	UpdatedAt *time.Time
	ClosedAt  *time.Time
	MergedAt  *time.Time
	SyncedAt  time.Time
}

// The states a pull request is filtered by.
const (
	PullRequestsOpen   = "open"
	PullRequestsClosed = "closed"
)

const pullRequestColumns = `id, project_id, number, title, author, source_branch, target_branch, head_sha,
	state, url, created_at, updated_at, closed_at, merged_at, synced_at`

func scanPullRequest(row pgx.Row) (PullRequest, error) {
	var pull PullRequest
	err := row.Scan(&pull.ID, &pull.ProjectID, &pull.Number, &pull.Title, &pull.Author,
		&pull.SourceBranch, &pull.TargetBranch, &pull.HeadSHA, &pull.State, &pull.URL,
		&pull.CreatedAt, &pull.UpdatedAt, &pull.ClosedAt, &pull.MergedAt, &pull.SyncedAt)
	return pull, err
}

// UpsertPullRequest records a pull request as the forge describes it, and returns what was recorded
// before, nil for a request seen for the first time. The two are read and written in one
// transaction: the transition between them is what the caller turns into a fact.
func (db *DB) UpsertPullRequest(ctx context.Context, pull PullRequest) (PullRequest, *PullRequest, error) {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return PullRequest{}, nil, fmt.Errorf("pull request #%d: %w", pull.Number, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var previous *PullRequest
	existing, err := scanPullRequest(tx.QueryRow(ctx,
		`select `+pullRequestColumns+` from pull_requests where project_id = $1 and number = $2 for update`,
		pull.ProjectID, pull.Number))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return PullRequest{}, nil, fmt.Errorf("pull request #%d: %w", pull.Number, err)
	default:
		previous = &existing
	}

	recorded, err := scanPullRequest(tx.QueryRow(ctx,
		`insert into pull_requests (project_id, number, title, author, source_branch, target_branch,
		                            head_sha, state, url, created_at, updated_at, closed_at, merged_at)
		 values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		 on conflict (project_id, number) do update
		 set title = excluded.title, author = excluded.author, source_branch = excluded.source_branch,
		     target_branch = excluded.target_branch, head_sha = excluded.head_sha, state = excluded.state,
		     url = excluded.url, created_at = excluded.created_at, updated_at = excluded.updated_at,
		     closed_at = excluded.closed_at, merged_at = excluded.merged_at, synced_at = now()
		 returning `+pullRequestColumns,
		pull.ProjectID, pull.Number, pull.Title, pull.Author, pull.SourceBranch, pull.TargetBranch,
		pull.HeadSHA, pull.State, pull.URL, pull.CreatedAt, pull.UpdatedAt, pull.ClosedAt, pull.MergedAt))
	if err != nil {
		return PullRequest{}, nil, fmt.Errorf("recording pull request #%d: %w", pull.Number, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return PullRequest{}, nil, fmt.Errorf("recording pull request #%d: %w", pull.Number, err)
	}
	return recorded, previous, nil
}

// ListPullRequests returns a project's pull requests, the most recently updated first. `state` is
// PullRequestsOpen, PullRequestsClosed (closed or merged) or empty for every one.
func (db *DB) ListPullRequests(ctx context.Context, projectID int64, state string, limit int) ([]PullRequest, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	filter := ""
	switch state {
	case PullRequestsOpen:
		filter = "and state = 'open'"
	case PullRequestsClosed:
		filter = "and state in ('closed', 'merged')"
	}
	rows, err := db.pool.Query(ctx,
		`select `+pullRequestColumns+` from pull_requests
		 where project_id = $1 `+filter+`
		 order by updated_at desc nulls last, number desc
		 limit $2`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("pull requests of project %d: %w", projectID, err)
	}
	defer rows.Close()

	var pulls []PullRequest
	for rows.Next() {
		pull, err := scanPullRequest(rows)
		if err != nil {
			return nil, fmt.Errorf("pull requests of project %d: %w", projectID, err)
		}
		pulls = append(pulls, pull)
	}
	return pulls, rows.Err()
}

// PullRequestByNumber returns a project's pull request.
func (db *DB) PullRequestByNumber(ctx context.Context, projectID, number int64) (PullRequest, error) {
	pull, err := scanPullRequest(db.pool.QueryRow(ctx,
		`select `+pullRequestColumns+` from pull_requests where project_id = $1 and number = $2`, projectID, number))
	if errors.Is(err, pgx.ErrNoRows) {
		return PullRequest{}, ErrPullRequestNotFound
	}
	if err != nil {
		return PullRequest{}, fmt.Errorf("pull request #%d: %w", number, err)
	}
	return pull, nil
}
