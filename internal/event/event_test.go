package event

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var instant = time.Date(2026, 9, 2, 14, 30, 0, 0, time.UTC)

/*
 * The deduplication rule.
 *
 * It is the package's whole subject. The observer redelivers the finished runs — informer
 * resynchronisation, worker restart — and a fact with no stable key would be re-emitted every
 * time. Today that would cost nothing; the day a reaction sends a notification, it would send one
 * per restart.
 */

// The same fact, observed twice, carries the same key.
//
// The tracker probe returns a snapshot: it re-reads every version on each pass,
// because the API does not filter them. This is therefore the nominal case, not a
// redelivery incident.
func TestTheSameFactCarriesTheSameKey(t *testing.T) {
	premier := VersionCreatedAt(instant, 805, "v3.2.0", "open", "redmine", "412", nil)
	// The next pass re-reads the same version, later, and its status has changed.
	second := VersionCreatedAt(instant.Add(time.Hour), 805, "v3.2.0", "closed", "redmine", "412", nil)

	if premier.DedupKey != second.DedupKey {
		t.Errorf("keys %q and %q — an already-seen version would be observed again",
			premier.DedupKey, second.DedupKey)
	}
}

// A version's key carries the local line, not the tracker's identifier.
//
// That is what makes the fact independent of the tool: two trackers connected to the
// same project do not observe the same version twice, and an instance changing tool
// would not re-observe its whole history.
func TestAVersionKeyCarriesTheLocalLine(t *testing.T) {
	redmine := VersionCreatedAt(instant, 805, "v3.2.0", "open", "redmine", "412", nil)
	autre := VersionCreatedAt(instant, 805, "v3.2.0", "open", "jira", "MSE-9", nil)

	if redmine.DedupKey != autre.DedupKey {
		t.Errorf("keys %q and %q — the same version would be observed twice",
			redmine.DedupKey, autre.DedupKey)
	}
	if !strings.Contains(redmine.DedupKey, "805") {
		t.Errorf("key %q — it must carry the local version", redmine.DedupKey)
	}
}

// Two facts of different kinds are never conflated, even on the same object: the key
// carries the type.
func TestTwoKindsAreNeverConflated(t *testing.T) {
	created := ReleaseCreatedAt(instant, 7, 42, "1.8.0-rc1", "pre", "main", "1.8.0")
	released := ReleaseReleasedAt(instant, 7, 42, "1.8.0-rc1", "vcarroy")

	if created.DedupKey == released.DedupKey {
		t.Errorf("key %q shared between two kinds", created.DedupKey)
	}
}

/*
 * What a fact must carry in order to be usable.
 */

// A fact with no key is refused, and the message says why.
//
// The check that counts: without it the insert would succeed and the flaw would only
// show once a reaction was connected, far from here.
func TestAFactWithoutAKeyIsRefused(t *testing.T) {
	fait := ReleaseCreatedAt(instant, 7, 42, "1.8.0-rc1", "pre", "main", "1.8.0")
	fait.DedupKey = ""

	err := fait.Validate()
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("validation: %v, expected ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "redelivery") {
		t.Errorf("message %q — it must state the consequence", err)
	}
}

// A fact with no instant is refused: the fact's date is what orders it, and zero
// would place it in 1970, before everything else.
func TestAFactWithoutAnInstantIsRefused(t *testing.T) {
	fait := ReleaseCreatedAt(instant, 7, 42, "1.8.0-rc1", "pre", "main", "1.8.0")
	fait.OccurredAt = time.Time{}

	if err := fait.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("validation: %v, expected ErrInvalid", err)
	}
}

// Every fact the constructors produce validates.
//
// A guard against forgetting: adding a constructor without giving it a key would
// otherwise go unnoticed until the first real emission.
func TestEveryConstructorProducesAValidFact(t *testing.T) {
	faits := everyFact()

	for _, fait := range faits {
		if err := fait.Validate(); err != nil {
			t.Errorf("fait %q invalide: %v", fait.Type, err)
		}
		if fait.Subject == "" {
			t.Errorf("fact %q with no subject — the screen would have nothing to show", fait.Type)
		}
		// No constructor fills in the source: it is the probe loop that names the plugin that
		// observed, and doing it here as well would give two places where they can diverge.
		if fait.Source != "" {
			t.Errorf("fact %q: source %q, expected empty — the probe is what sets it",
				fait.Type, fait.Source)
		}
		if fait.Depth != 0 {
			t.Errorf("fact %q: depth %d, expected zero — nothing reacts yet",
				fait.Type, fait.Depth)
		}
	}
}

/*
 * The reduction of the core's facts.
 *
 * A core fact exists when **something happens that no plan caused**. Three facts fell to that
 * rule: `release.built`, `release.verified`, `pipeline.finished` — internal steps, which surfaced
 * in the feed only to go straight back in.
 *
 * The rule is not checkable by a machine; the **count** is. This test does not say that a fifth
 * fact would be wrong, it says that it must not arrive without having been decided.
 *
 * `release.promoted` left in its turn: it is the MSE plugin that defines what a promotion is, so
 * it is the plugin that observes it.
 */
func TestTheCoreHasOnlyThreeFacts(t *testing.T) {
	attendus := map[Type]bool{
		VersionCreated: true, ReleaseCreated: true, ReleaseReleased: true,
	}

	for _, fait := range everyFact() {
		if !attendus[fait.Type] {
			t.Errorf("fact %q outside the three core ones — does a plan cause it?", fait.Type)
		}
		delete(attendus, fait.Type)
	}
	for manquant := range attendus {
		t.Errorf("fact %q no longer has a constructor", manquant)
	}
}

// A version with no due date does not carry an empty one.
//
// A `due_date=` match would recognise the empty string, whereas no due date is not a
// due date.
func TestAVersionWithNoDueDateDoesNotCarryAnEmptyOne(t *testing.T) {
	fait := VersionCreatedAt(instant, 805, "v3.2.0", "open", "redmine", "412", nil)

	if _, present := fait.Payload["due_date"]; present {
		t.Errorf("invented due date: %v", fait.Payload)
	}
}

// everyFact returns one instance of each core constructor.
func everyFact() []Event {
	echeance := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	return []Event{
		VersionCreatedAt(instant, 805, "v3.2.0", "open", "redmine", "412", &echeance),
		ReleaseCreatedAt(instant, 7, 42, "1.8.0-rc1", "pre", "main", "1.8.0"),
		ReleaseReleasedAt(instant, 7, 42, "1.8.0", "vcarroy"),
	}
}
