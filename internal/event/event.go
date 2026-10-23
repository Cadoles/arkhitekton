// Package event names the facts the dashboard observes.
//
// A fact, never an order. `release.created` says a release was declared; it does not
// say what to do. The distinction is not cosmetic: a fact is replayable — it can be
// re-read, re-emitted, archived — where an order is not, and that property is what
// lets reactions hang off it without the core knowing them.
//
// A core fact exists when something happens that no plan caused. What a plan causes
// belongs to the plan: it has no business reappearing in the fact feed only to
// re-enter through a reaction.
//
// Three facts were removed on that rule. Two were stages of a release's plan, and it
// is now the stage's role that advances its state; the third was a run's generic
// conclusion, and `when: failure` on a stage covers what it was asked for.
//
// Three entry points remain: a version declared in the tracker, a release declared,
// a release approved. The last could have fallen under the same rule; it does not,
// because it is a human decision taken outside any automation.
//
// Successful promotion left on another ground: the extension that deploys defines what a
// promotion is, and it is the extension that observes it.
//
// This package emits nothing. It carries the vocabulary and the deduplication rule,
// nothing else. The emission points live where the fact occurs: the api for human
// gestures, the worker for what it observes. The one thing that must truly be shared
// is how a fact is named — two ways would produce two keys for the same event, and
// deduplication would no longer serve.
//
// Deduplication is the heart of the package because the observer redelivers finished
// runs — informer resynchronisation, worker restart — and the probes do worse: the
// tracker probe re-reads every version on each pass, because the API does not filter
// them. A fact with no stable key would be re-emitted on every round. Today nobody
// listens and it would cost nothing; the day a reaction sends a notification, it
// would send one per worker restart.
package event

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Type names a fact.
//
// Core types are bare — `release.created` — where a plugin's are prefixed by its
// identifier: `gitea.pull_request.opened`. Two forge plugins will coexist, and a
// reaction must be able to say which one it listens to.
type Type string

const (
	// VersionCreated: a version appears in the project's tracker.
	//
	// A core fact although observed by a plugin, deliberately: every tracker writes its
	// versions to the same table, and a reaction declared on "a version exists" need not
	// be rewritten when the deployment changes tool. The plugin that observed it is
	// still named by [Event.Source].
	VersionCreated Type = "version.created"
	// ReleaseCreated: a release is declared. The first fact of its life.
	ReleaseCreated Type = "release.created"
	// ReleaseReleased: a release is approved for shipping. The only transition nothing
	// automates.
	ReleaseReleased Type = "release.released"
)

// Event is an observed fact.
type Event struct {
	Type Type
	// Source names who observed it: the plugin that saw it, or nothing when the core
	// observed it itself.
	//
	// It is therefore not quite the same as the type's prefix, and `version.created` is
	// the exception that shows it: a core fact, observed by a plugin's probe, carrying
	// its identifier without carrying its prefix.
	//
	// Explicit rather than inferred from the name: a plugin removed in a later version
	// leaves facts that must still be attributable, and recognising them by their type
	// would mean knowing the plugins that disappeared.
	Source string
	// ProjectID attaches the fact to its project. Nil for a fact aiming at none.
	ProjectID *int64
	// Subject names what the fact is about, in the screen's vocabulary: a release tag, a
	// run name. It serves to read the fact, never to find it — that is the payload's
	// role.
	Subject string
	// Payload is a flat map of strings, like plugin configuration and deployer settings.
	//
	// Flat because a reaction's match is on a value, not on a path: `kind = final` reads
	// and edits, `$.release.kind` would need a path language and the interface that goes
	// with it.
	Payload map[string]string
	// OccurredAt is the instant of the fact, never of its reading. The distinction will
	// matter for probes catching up on days of history: dating them at reading time
	// would make everything look as though it happened at once.
	OccurredAt time.Time
	// Depth is the chaining depth, inherited from the fact that caused it.
	//
	// Zero while nothing reacts. The field exists now because it bounds loops, and a
	// field added later leaves a history whose depth is unknown.
	Depth int
	// DedupKey identifies the fact, not its reading. Two emissions of the same fact carry
	// the same key, and the second writes nothing.
	DedupKey string
}

// ErrInvalid reports a fact we refuse to record.
var ErrInvalid = errors.New("invalid fact")

// Validate refuses a fact that could not be used.
//
// The deduplication key is the check that counts: without it the insert would succeed
// and the fact would be re-emitted on every redelivery. The flaw would only show once
// a reaction was connected, far from here.
func (e Event) Validate() error {
	var problems []error

	if strings.TrimSpace(string(e.Type)) == "" {
		problems = append(problems, fmt.Errorf("%w: type missing", ErrInvalid))
	}
	if strings.TrimSpace(e.DedupKey) == "" {
		problems = append(problems, fmt.Errorf(
			"%w: deduplication key missing for %q — the fact would be re-emitted on "+
				"every redelivery", ErrInvalid, e.Type))
	}
	if e.OccurredAt.IsZero() {
		problems = append(problems, fmt.Errorf("%w: instant missing for %q", ErrInvalid, e.Type))
	}
	if e.Depth < 0 {
		problems = append(problems, fmt.Errorf("%w: negative depth (%d)", ErrInvalid, e.Depth))
	}

	return errors.Join(problems...)
}

// key composes a deduplication key.
//
// Returned here and never in the caller: it is the one thing the two emitters — the
// api and the worker — must absolutely share, and two ways of composing it would
// produce two keys for the same fact.
func key(parts ...string) string {
	return strings.Join(parts, ":")
}

/*
 * The constructors.
 *
 * One per fact, rather than a generic factory: it is here that the deduplication rule lives, and
 * writing it once per fact is what makes **what identifies that fact** readable. A single factory
 * would have sent that decision back to each caller, that is, to the place where it gets it wrong.
 */

// VersionCreatedAt returns the fact of a version appearing in the tracker.
//
// The key carries the local version, not the tracker's. That is what makes the fact
// independent of the tool that observed it. An instance migrating to another tracker
// would otherwise re-observe its whole history under fresh identifiers — and fire one
// notification per version.
//
// It also guards against the shape of this probe: the tracker re-reads every version
// on each pass, because the API does not filter them. It is the key's uniqueness, and
// it alone, that keeps an already-seen version from being observed again.
func VersionCreatedAt(
	at time.Time, versionID int64, name, status, tool, externalID string, dueDate *time.Time,
) Event {
	payload := map[string]string{
		"version_id": id(versionID), "name": name, "status": status,
		// The tool and its identifier: what allows going back to the record, and telling
		// apart two trackers connected to the same project.
		"tool": tool, "external_id": externalID,
	}
	// A missing due date stays missing. Returning it as an empty string would make a
	// `due_date=` match recognise it, whereas no due date is not a due date.
	if dueDate != nil {
		payload["due_date"] = dueDate.Format(time.DateOnly)
	}

	return Event{
		Type: VersionCreated, Subject: name,
		OccurredAt: at,
		DedupKey:   key(string(VersionCreated), id(versionID)),
		Payload:    payload,
	}
}

// ReleaseCreatedKey returns the key of the fact of a release being declared.
//
// Exported so that opening the plan and emitting the fact compose the same key: it is
// what makes the run unique per release, and two spellings would open two.
func ReleaseCreatedKey(releaseID int64) string {
	return key(string(ReleaseCreated), id(releaseID))
}

// ReleaseCreatedAt returns the fact of a release being declared.
//
// Keyed on the release alone: a release is created only once, and a second call would
// be a duplicate that the uniqueness constraint already refuses upstream.
func ReleaseCreatedAt(
	at time.Time, projectID, releaseID int64, tag, kind, gitRef, version string,
) Event {
	return Event{
		Type: ReleaseCreated, ProjectID: &projectID, Subject: tag,
		OccurredAt: at,
		DedupKey:   ReleaseCreatedKey(releaseID),
		Payload: map[string]string{
			"release_id": id(releaseID), "tag": tag, "kind": kind,
			"git_ref": gitRef, "version": version,
		},
	}
}

// ReleaseReleasedAt returns the fact of a release approved for shipping.
func ReleaseReleasedAt(at time.Time, projectID, releaseID int64, tag, by string) Event {
	return Event{
		Type: ReleaseReleased, ProjectID: &projectID, Subject: tag,
		OccurredAt: at,
		DedupKey:   key(string(ReleaseReleased), id(releaseID)),
		Payload: map[string]string{
			"release_id": id(releaseID), "tag": tag, "released_by": by,
		},
	}
}

func id(value int64) string { return fmt.Sprintf("%d", value) }
