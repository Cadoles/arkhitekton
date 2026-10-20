package capability

import (
	"context"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

/*
 * What a plugin brings beside pipelines: a connection to a tracker or a forge, probes that observe
 * them, and the actions it declares.
 *
 * The dashboard keeps the store, the cursors, the deduplication and the reactions. A plugin is
 * handed a [Target], its settings already resolved and unsealed, and returns what it read or did.
 * It never sees the database, and it never chooses whom it talks to: the target comes from the
 * settings an administrator typed.
 */

// Settings are a plugin's settings for one project, the project's layered over the instance's,
// sensitive fields unsealed.
type Settings map[string]string

// Target is what a plugin is given to reach its boundary for one project.
type Target struct {
	// Project is the project's slug, for messages.
	Project string
	// Repository is the project's repository, `owner/name`, empty when it declares none.
	Repository string
	Settings   Settings
	// Timeout bounds each call to the outside, set by the deployment.
	Timeout time.Duration
}

// TrackerCapability connects to a tracker of versions and issues.
type TrackerCapability interface {
	Connect(target Target) (provider.Tracker, error)
	// VersionURL and IssueURL link a version or an issue in the tracker's own interface. Empty
	// when the settings name no instance.
	VersionURL(settings Settings, id string) string
	IssueURL(settings Settings, id string) string
}

// ForgeCapability connects to a forge, bound to the project's repository.
type ForgeCapability interface {
	Connect(target Target) (provider.Forge, error)
	// Writer creates tags. A separate connection: reading needs less than writing, and a token
	// scoped for reading must still let the forge's pages work.
	Writer(target Target) (provider.TagWriter, error)
	// Prober checks a repository before a project is saved with it.
	Prober(target Target) (provider.RepositoryProber, error)
}

// Schedule says when a probe wants to be queried. The deployment's floor wins over a shorter
// interval.
type Schedule struct {
	Interval time.Duration
	// Eager asks for a pass at start-up, for a source that fills a screen.
	Eager bool
}

// Probe observes an outside source and returns the facts it sees there.
//
// It reads and reports: the dashboard decides what is written and what triggers, and its first
// pass on a project triggers nothing.
type Probe interface {
	// Name names the probe's cursor within its plugin.
	Name() string
	Schedule() Schedule
	// Poll returns the facts that occurred since the cursor, and the next cursor. An empty cursor
	// is a first pass.
	Poll(ctx context.Context, target Target, cursor string) ([]Observation, string, error)
}

// Observation is a fact a probe saw.
type Observation struct {
	// Suffix is one of the plugin's declared [EventSpec] suffixes. The dashboard prefixes it with
	// the plugin's identifier and refuses an undeclared one.
	Suffix  string
	Subject string
	Payload map[string]string
	// OccurredAt is the fact's own date, never the reading's.
	OccurredAt time.Time
	// Key identifies the fact among the plugin's facts of this type: reading it twice must not
	// produce two facts.
	Key string
}

// ActionExecutor carries out the actions a plugin declares.
type ActionExecutor interface {
	// Execute carries out the action of this suffix with parameters already interpolated and
	// checked against the declaration.
	Execute(ctx context.Context, target Target, suffix string, params map[string]string) error
}
