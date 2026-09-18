package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The probes: where a plugin's facts come from.
 *
 * # A probe rather than reception
 *
 * "Detecting" hides two very different mechanisms. The dashboard queries, or the forge pushes.
 * The probe was chosen for three reasons that hold together:
 *
 *   - the machinery exists — the tracker synchronisation does exactly that, and `sync_state`
 *     already carries `(source, project_id)`;
 *   - reception would add a **public entry point**, unauthenticated by nature, requiring a
 *     shared signature and an anti-replay window: a decision of another kind, not a point of
 *     extension;
 *   - the same debate was already settled this way for the tracker.
 *
 * # The three obligations, held **here** and not by each probe
 *
 * They live in the loop, deliberately: an obligation handed to each implementation is an
 * obligation one implementation will forget.
 *
 *  1. **The first pass emits nothing.** It establishes the cursor and stops. Without that,
 *     enabling a probe on a repository carrying four hundred pull requests emits four hundred
 *     facts, therefore triggers four hundred plans, therefore sends four hundred notifications.
 *     Enabling something does not wake the past.
 *  2. **The deduplication key comes from the probe.** It alone knows what identifies its facts
 *     — for a pull request, its number. The loop re-reads an overlap window at each pass; with
 *     no stable key, every pass would re-notify.
 *  3. **A failure is isolated per project.** Two projects may target two forges, and one's
 *     outage says nothing about the other. It is the rule the tracker synchronisation has just
 *     learnt.
 */

// Schedule says when a probe wants to be queried.
//
// Declared by the probe and not imposed by the loop: the tracker moves at the pace of human
// typing and is re-read every five minutes, a forge is watched more often. A single cadence
// would serve both badly — and the deployment's floor stays above everything.
type Schedule struct {
	// Interval is the period wanted. Raised to the deployment's floor: a probe cannot ask to query
	// a third-party machine more often than operations has allowed.
	Interval time.Duration
	// Eager asks for an immediate pass at start-up.
	//
	// False by default, and that is the right default: a probe that only observes has nothing to
	// catch up, and a restart loop would hammer a machine that is not ours. True for a source that
	// **fills a screen** — the versions screen would be empty until the first beat.
	Eager bool
}

// Probe observes an outside source and returns the facts it sees there.
//
// A narrow interface: the probe does **not** persist its facts, does not deduplicate them,
// does not date them of its own accord. It reads and reports; the loop decides what is written
// and what triggers.
//
// What it does with its **own** data is none of the loop's business. The tracker
// synchronisation projects versions and requests into its tables as it goes: that is its work,
// and forbidding it would have needed a second mechanism to run exactly the same loop.
type Probe interface {
	// PluginID says which plugin the probe gets its connection and its activation from. A
	// switched-off plugin — on the instance or on the project — is not queried.
	PluginID() string
	// Source names the cursor in `sync_state`. Specific to the probe and not to the plugin: a
	// plugin may carry several, and a shared cursor would have one advance the other's.
	Source() string
	// Schedule says at what cadence to query, and whether to do it straight away.
	Schedule() Schedule
	// Poll returns the facts that occurred since the cursor, and the next cursor.
	//
	// The facts carry their **own** deduplication key and their **own** date — the fact's, never
	// the read's. A probe catching up three days of history must produce facts dated across those
	// three days, otherwise everything appears to have happened at once and the order is lost.
	Poll(ctx context.Context, project store.Project, config map[string]string,
		cursor string) ([]event.Event, string, error)
}

// ProbeStore is what the loop needs from the database.
type ProbeStore interface {
	ListProjects(ctx context.Context) ([]store.Project, error)
	ResolvedPlugin(ctx context.Context, projectID int64, pluginID string) (store.PluginSettings, error)
	GetSyncState(ctx context.Context, source string, projectID int64) (store.SyncState, error)
	RecordSyncSuccess(ctx context.Context, source string, projectID int64, cursor string) error
	RecordSyncFailure(ctx context.Context, source string, projectID int64, message string) error
	RecordEvent(ctx context.Context, fact event.Event) (bool, error)
}

// minProbeInterval is the querying floor.
//
// The load can be counted: *projects × probes × frequency*, and the multiplicand is a machine
// that is not ours. A probe querying a forge every ten seconds for thirty projects would be an
// unintended denial of service.
const minProbeInterval = time.Minute

// ProbeRunner runs every project's probes.
type ProbeRunner struct {
	store   ProbeStore
	secrets SecretOpener
	probes  []Probe
	// dispatcher triggers the declared reactions of a new fact. Nil: facts are observed and nobody
	// reacts to them.
	dispatcher *Dispatcher
	interval   time.Duration
	logger     *slog.Logger
	now        func() time.Time
}

// WithDispatcher wires the routing of facts towards the declared reactions.
func (r *ProbeRunner) WithDispatcher(dispatcher *Dispatcher) *ProbeRunner {
	r.dispatcher = dispatcher
	return r
}

func NewProbeRunner(
	probes ProbeStore, opener SecretOpener, interval time.Duration,
	logger *slog.Logger, sondes ...Probe,
) *ProbeRunner {
	if interval < minProbeInterval {
		interval = minProbeInterval
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ProbeRunner{
		store: probes, secrets: opener, probes: sondes,
		interval: interval, logger: logger, now: time.Now,
	}
}

// Run queries the probes until cancellation.
//
// One loop **per probe**, each at its own cadence. A common pass would force the slowest and
// the fastest to agree, and the tracker — five minutes — would then lay down the law for a
// forge one wants to watch more often.
func (r *ProbeRunner) Run(ctx context.Context) error {
	if len(r.probes) == 0 {
		// Nothing to query. The loop does not spin for nothing: it would produce a round of log per
		// interval to do nothing.
		<-ctx.Done()
		return ctx.Err()
	}

	var pending sync.WaitGroup
	for _, probe := range r.probes {
		pending.Add(1)
		go func(probe Probe) {
			defer pending.Done()
			r.loop(ctx, probe)
		}(probe)
	}
	pending.Wait()
	return ctx.Err()
}

// loop runs a probe at its cadence.
func (r *ProbeRunner) loop(ctx context.Context, probe Probe) {
	schedule := probe.Schedule()
	// The deployment's floor wins: a probe declares its cadence above it, never below.
	interval := schedule.Interval
	if interval < r.interval {
		interval = r.interval
	}

	// An immediate pass only if the probe asks for one. A source that fills a screen wants it; a
	// source that only observes has nothing to catch up, and waiting stops a restart loop
	// hammering a third-party machine.
	if schedule.Eager {
		r.sweep(ctx, probe)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.sweep(ctx, probe)
		}
	}
}

// sweep runs a probe over every project.
func (r *ProbeRunner) sweep(ctx context.Context, probe Probe) {
	projects, err := r.store.ListProjects(ctx)
	if err != nil {
		r.logger.Error("projects to probe", "error", err)
		return
	}

	for _, project := range projects {
		// One project's failure **does not stop** the next ones: two projects may target two servers,
		// and one's outage says nothing about the other.
		if err := r.pollOne(ctx, probe, project); err != nil {
			r.logger.Error("sonde",
				"source", probe.Source(), "project", project.Slug, "error", err)
			if recordErr := r.store.RecordSyncFailure(
				ctx, probe.Source(), project.ID, err.Error()); recordErr != nil {
				r.logger.Error("recording a probe's failure",
					"source", probe.Source(), "project", project.Slug, "error", recordErr)
			}
		}
	}
}

// pollOne queries a probe for one project.
func (r *ProbeRunner) pollOne(ctx context.Context, probe Probe, project store.Project) error {
	settings, err := r.store.ResolvedPlugin(ctx, project.ID, probe.PluginID())
	if err != nil {
		if errors.Is(err, store.ErrPluginDisabled) {
			// Switched off on the instance or on the project. It is not an outage: reporting it at every
			// pass would fill the log with a wanted state.
			return nil
		}
		return err
	}

	config, err := r.openSecrets(probe.PluginID(), settings.Config)
	if err != nil {
		return err
	}

	state, err := r.store.GetSyncState(ctx, probe.Source(), project.ID)
	if err != nil {
		return err
	}

	facts, cursor, err := probe.Poll(ctx, project, config, state.Cursor)
	if err != nil {
		return err
	}

	// **The first pass triggers nothing.**
	//
	// It is the least obvious rule and the most expensive to discover: without it, enabling a
	// probe on a repository carrying four hundred pull requests would send four hundred
	// notifications. Enabling something does not wake the past.
	//
	// # It records nonetheless, and that is a fix
	//
	// The bootstrap pass **writes** the facts, without routing them. Discarding them would have
	// been enough as long as every probe filtered on its cursor — the Gitea probe returns only the
	// requests opened since, and does not see them again.
	//
	// A probe that returns a **snapshot** breaks that assumption: the tracker re-reads every
	// version at each pass, because Redmine's API does not filter them. Discarded at bootstrap
	// without being kept, those facts would be new at the next pass — and would all trigger.
	// "Enabling something does not wake the past" would have been true for one round only.
	priming := state.Cursor == ""

	emitted := 0
	for _, fact := range facts {
		fact.ProjectID = &project.ID
		fact.Source = probe.PluginID()

		recorded, err := r.store.RecordEvent(ctx, fact)
		if err != nil {
			// A fact that could not be written does not stop the others: the cursor does not advance
			// either, and the next pass will see it again.
			return fmt.Errorf("fait %q: %w", fact.Type, err)
		}
		if !recorded || priming {
			continue
		}
		emitted++
		// On a **new** fact only: the overlap window makes the same requests come round at every pass,
		// and triggering on each would send one notification per round.
		r.dispatcher.Dispatch(ctx, fact)
	}

	switch {
	case priming:
		r.logger.Info("probe bootstrapped, no reaction triggered",
			"source", probe.Source(), "project", project.Slug,
			"faits_retenus", len(facts),
			"reason", "enabling something does not wake the past")
	case emitted > 0:
		r.logger.Info("facts observed by a probe",
			"source", probe.Source(), "project", project.Slug,
			"emis", emitted, "lus", len(facts))
	}
	return r.store.RecordSyncSuccess(ctx, probe.Source(), project.ID, cursor)
}

// openSecrets unseals the sensitive fields of a plugin's configuration.
//
// It is the only place where they are in clear text, and they do not leave it: the map
// returned is consumed by the probe for the duration of its call.
func (r *ProbeRunner) openSecrets(
	pluginID string, sealed map[string]string,
) (map[string]string, error) {
	return openPluginSecrets(r.secrets, pluginID, sealed)
}

// openPluginSecrets unseals a plugin's configuration, for whoever needs it.
//
// Shared between the probes and the plugin actions since both reach the same boundary with the
// same connection. Writing two versions of it would make two places to decide what is done
// with a field that could not be opened — and the second would end up letting it through
// empty.
func openPluginSecrets(
	opener SecretOpener, pluginID string, sealed map[string]string,
) (map[string]string, error) {
	config := make(map[string]string, len(sealed))
	for name, value := range sealed {
		if !secretField(pluginID, name) {
			config[name] = value
			continue
		}
		if opener == nil || !opener.Configured() {
			if strings.TrimSpace(value) == "" {
				continue
			}
			return nil, fmt.Errorf(
				"the field %q is sealed and sealing is not configured", name)
		}
		opened, err := opener.Open(value)
		if err != nil {
			// The message does not quote the value, and for good reason: that is the one we could not
			// open.
			return nil, fmt.Errorf("champ %q illisible: %w", name, err)
		}
		config[name] = opened
	}
	return config, nil
}

// secretField says whether a plugin field carries a sealed value.
//
// Read from the registry rather than written here: two lists would diverge at the first field
// added, and the gap would show up as a sealed value passed as it is to a forge — which would
// refuse it with an authentication message that would not say the value was never opened.
func secretField(pluginID, field string) bool {
	descriptor, err := plugin.Get(pluginID)
	if err != nil {
		return false
	}
	// Both scopes: a sensitive field declared globally is sensitive too when a project overrides
	// it, and the reverse is true.
	return descriptor.IsSecret(plugin.ScopeGlobal, field) ||
		descriptor.IsSecret(plugin.ScopeProject, field)
}
