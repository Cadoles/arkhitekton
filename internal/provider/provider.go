// Package provider names the boundaries with the outside tools.
//
// The dashboard reads three sources it does not drive: a **tracker** of versions and requests, a
// **forge** of repositories and references, and an **attestation** of what is really running.
// Each has a single implementation today — Redmine, Gitea, the `PortalData` CRD — and this
// package does not seek to add any.
//
// What it brings is more modest and more useful: one place that says what being one of these
// sources **means**. Until now the answer was scattered across half a dozen narrow interfaces
// declared by their callers — good Go idiom, but nobody could say, without reading them all,
// what a second tracker would have to supply. The cost of that scattering is not paid when
// decoupling: it is paid at every addition, when a method specific to Redmine enters a handler
// with nothing reporting it.
//
// # What this package is not
//
// Not a plugin mechanism. No registry, no `init()`, no selection at run time. The configuration
// still names Redmine and Gitea literally, and the binary can build only those. The day a second
// provider exists, this is where we will know what to ask of it — not before.
//
// Nor a layer of indirection to cross. The callers keep declaring the narrow interface they need,
// and receiving the concrete client. This package carries the **vocabulary** they exchange and
// the complete contract each client honours; the conformance assertions, placed next to each
// implementation, make the contract break at compile time rather than diverge silently.
//
// # What is missing, and why
//
// `Deployer` — promote, observe, prove — is not here. It requires answering "what does promoting
// mean", and that answer is precisely what the analysis showed cannot be settled in the abstract.
// Writing it now would produce an interface modelled on Flux, which one would believe generic.
//
// Nor is `Exporter`: the delivery note is a contractual deliverable whose shape belongs to the
// project, and it has no outside source to abstract.
package provider

import (
	"context"
	"errors"
	"strconv"
	"time"
)

/*
 * The errors every implementation shares.
 *
 * Each package carried its own — `gitea.ErrTagExists`, `redmine.ErrNotConfigured`,
 * `gitea.ErrWriteForbidden` — so a caller wanting to interpret them had to import the
 * implementation. The HTTP handler compared literally against `gitea.ErrTagExists`: a boundary
 * crossed for a single comparison, and a second forge would have returned an error this code
 * would not have recognised — setting a tag would have failed as an "internal error" where it had
 * simply already been done.
 *
 * The implementations **wrap** them rather than return them bare: the message keeps the tool's
 * name, which is what one wants to read in a log, while `errors.Is` recognises the error, which
 * is what the code needs.
 */
var (
	// ErrNotConfigured reports that a source has nothing to be reached with.
	//
	// Not an outage: an unconfigured forge or tracker is a valid installation, where the dashboard
	// falls back to free-text entry and says so on screen. The caller must be able to tell the
	// difference.
	ErrNotConfigured = errors.New("access not configured")

	// ErrWriteForbidden reports a write attempted by a read access.
	//
	// The dashboard separates reading and writing into two clients carrying two permissions. This
	// error is the first one's guard rail: it fires in the transport, before any network call, and
	// reports a wiring fault rather than a refusal by the remote server.
	ErrWriteForbidden = errors.New("writing forbidden, this access is read-only")

	// ErrTagExists reports a tag already set.
	//
	// Distinct from a write error: setting a tag again after a partial failure is a **normal** case,
	// and the caller must be able to continue rather than stop.
	ErrTagExists = errors.New("the tag already exists")
)

// Account is the identity behind an access key.
//
// Returned by both authenticated sources, for the same reason: knowing that a key works without
// waiting for a form to use it, and seeing whether it belongs to an administration account. An
// over-powerful token stays over-powerful even unused, and better to know it at start-up.
type Account struct {
	// ID is the numeric identifier when the source exposes one. Zero otherwise — Gitea returns none
	// on `/user`, and inventing a value would suggest an identity that was not read.
	ID    int64
	Login string
	Admin bool
}

// Tracker is the source of the versions and the requests.
//
// Read-only, and that is structuring: the dashboard never writes into the tracker. It matches
// what it reads there with what it observes, and leaves the correction to the tool that is
// authoritative. An implementation that wrote would step outside the contract.
type Tracker interface {
	// ListVersions returns a project's declared versions.
	ListVersions(ctx context.Context, project string) ([]Version, error)
	// ListIssues returns the requests matching the query, all pages together. Pagination belongs to
	// the implementation: the caller asks for a scope, not a page.
	ListIssues(ctx context.Context, query IssueQuery) ([]Issue, error)
	// CheckAccess validates the key and returns the account it belongs to. Must be free of side
	// effects: it is called at start-up, and a check that modified something would make the start-up
	// observable from outside.
	CheckAccess(ctx context.Context) (Account, error)
}

// Version is a version declared in the tracker.
//
// `Name` is kept as it is. A tracker often carries several conventions at once — `MSE3-1.5`,
// `MSE 3.1.0`, `v3.2.0` at MSE's — and normalising at write time would lose the information
// without solving anything. The identity is `ID`, never the name.
type Version struct {
	// ID is the identifier in the tracker, as **text**, for the same reason as a request's: nothing
	// guarantees a tool numbers.
	ID          string
	Name        string
	Description string
	// Status is `open`, `locked` or `closed`. Redmine's vocabulary, taken as it is: the dashboard
	// only displays and sorts by it, and translating it into a vocabulary of its own would create a
	// mapping table to maintain for no added information.
	Status    string
	DueDate   *time.Time
	UpdatedOn *time.Time
}

// Issue is a request, reduced to what the dashboard displays.
//
// The description is not taken: it carries the business detail, sometimes naming people, and the
// dashboard links back to the tracker to consult it. Not copying what one does not need is the
// simplest way of not having to protect it.
type Issue struct {
	// ID is the identifier in the tracker, as **text**: Redmine numbers, Jira writes `MSE-1234`. An
	// integer closed the door on the second with no error saying so.
	ID string
	// VersionID names the version targeted, by its identifier in the tracker.
	VersionID *string
	Tracker   string
	Subject   string
	Status    string
	// StatusClosed is the source's flag, taken without judgement. Mapping it to
	// `todo`/`doing`/`done`/`rejected` is a decision of the dashboard, carried by an editable table,
	// and the two may diverge: the instance marks `Ré-ouvert` as closed, which looks like a
	// configuration mistake.
	StatusClosed bool
	Priority     string
	Assignee     string
	DoneRatio    int
	UpdatedOn    *time.Time
}

// IssueQuery bounds a read of requests.
type IssueQuery struct {
	// Project is the project's identifier or shorthand.
	Project string
	// Since restricts to the requests modified since that instant. Zero to read everything, which
	// happens only at the first synchronisation.
	Since time.Time
}

// Forge is the source of the repositories, the references and the pull requests.
//
// Bound to **one** repository, the configuration's: the methods do not take its name as a
// parameter. Testing a repository one has just declared is [RepositoryProber]'s business, which
// does not have that constraint.
//
// **Read-only**. Writing — setting a tag — is a distinct permission, carried by [TagWriter] and
// by another client: the separation exists so that a deployment can configure reading without
// ever granting writing, and merging it here would make it disappear.
type Forge interface {
	// DefaultBranch returns the repository's default branch.
	DefaultBranch(ctx context.Context) (string, error)
	// ListBranches and ListTags return the references and say whether the reading bound was reached.
	// The second return is not decorative: a list silently truncated would suggest a reference does
	// not exist.
	ListBranches(ctx context.Context) ([]Ref, bool, error)
	ListTags(ctx context.Context) ([]Ref, bool, error)
	// ResolveCommit resolves a reference into a commit SHA.
	ResolveCommit(ctx context.Context, ref string) (string, error)
	// ListMergedPullRequests returns the requests merged since an instant, from the most recent to
	// the oldest.
	ListMergedPullRequests(ctx context.Context, since time.Time) ([]PullRequest, error)
	// LoadCommitMessages completes a request with its commits' messages. Separate from the listing
	// because it is one round trip per request: reading them by default would cost hundreds of
	// requests for a matching that needs only a few.
	LoadCommitMessages(ctx context.Context, pull *PullRequest) error
	// CheckAccess validates the token and returns the account it belongs to.
	CheckAccess(ctx context.Context) (Account, error)
}

// Probe is what an access test observed.
//
// Two distinct observations, and they are the type's subject: does the dashboard **read** this
// repository, and **is** its deploy key set there? The first is done by token and over HTTPS, the
// second looks at what the forge declares. Confusing them would make a green test read as a
// guarantee of cloning, which it has never been.
type Probe struct {
	// DefaultBranch is the repository's default branch.
	DefaultBranch string
	// KeyPresent says whether the public half appears in the repository's deploy keys.
	//
	// **Three states, not two.** Nil means "the question could not be asked" — a token with no
	// administration right on the repository, a forge that does not expose its keys, or a key not
	// produced yet — and it is not the same thing as "the key is absent". Answering false in that
	// case would cut the builds of a project whose key may well be in place.
	KeyPresent *bool
	// KeyNote says what the question answered, above all when it did not conclude: without it, a
	// `nil` would send one hunting at random.
	KeyNote string
}

// RepositoryProber tests access to an **arbitrary** repository.
//
// Outside [Forge], and not out of convenience: a Forge is bound to a repository at its
// construction, whereas the test bears precisely on a repository one has just declared and does
// not yet know to be reachable. Merging them would force building a client in order to discover
// that one cannot.
type RepositoryProber interface {
	// Probe returns the observation, or the error that prevents access to the repository. A
	// repository with no default branch exists but never received a commit: that is a failure,
	// because a pipeline's clone would fail there later.
	//
	// `publicKey` is the public half expected in the deploy keys. Empty, the question is not asked:
	// the project has no key yet, and looking for an empty key would always return "absent".
	Probe(ctx context.Context, repository, publicKey string) (Probe, error)
}

// TagWriter sets a Git tag.
//
// Separate from [Forge]: reading the forge and writing to it are two permissions, carried by two
// clients. The implementation is nil when the token cannot write, and the dashboard can then say
// so instead of failing at tagging time.
type TagWriter interface {
	CreateTag(ctx context.Context, tag, commit, message string) error
}

// Ref is a branch or a tag.
type Ref struct {
	Name string
	// SHA of the commit targeted. Informative: a release's `git_ref` field carries the name,
	// resolving it to a commit belongs to the pipeline at build time.
	SHA string
}

// PullRequest is a completed pull request, reduced to what serves the matching with the tracker's
// requests.
type PullRequest struct {
	Number   int64
	Title    string
	Body     string
	MergedAt *time.Time
	// Commits carries the commits' messages. Filled by LoadCommitMessages: the list of requests does
	// not contain them.
	Commits []string
}

// Reference returns `#42`, as one writes it in a note.
func (p PullRequest) Reference() string {
	return "#" + strconv.FormatInt(p.Number, 10)
}
