package store

import "fmt"

// Scope names whose results are read: a release's or a pull request's. The column is one of two
// fixed names, never a caller's string, which is what lets the queries compose it.
type Scope struct {
	column string
	ID     int64
}

// ReleaseScope reads a release's results.
func ReleaseScope(releaseID int64) Scope { return Scope{column: "release_id", ID: releaseID} }

// PullRequestScope reads a pull request's results.
func PullRequestScope(pullRequestID int64) Scope {
	return Scope{column: "pull_request_id", ID: pullRequestID}
}

// on returns the predicate selecting the scope's rows of a table aliased `alias`, on parameter $1.
func (s Scope) on(alias string) string { return alias + "." + s.column + " = $1" }

func (s Scope) String() string {
	if s.column == "pull_request_id" {
		return fmt.Sprintf("pull request %d", s.ID)
	}
	return fmt.Sprintf("release %d", s.ID)
}
