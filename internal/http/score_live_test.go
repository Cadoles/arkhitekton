package http

import (
	"context"
	"os"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/builtin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/results"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The score of a real release, computed on the real database.
 *
 * # Why this test exists
 *
 * The score by step rests on six fixture tests, one of them checked by mutation. None of them
 * runs the **attachment query** — the one following the chain result → run → job → step — and
 * that is precisely the piece only real data can disprove.
 *
 * This work has already yielded four defects the tests did not see: the shape of a deploy-key
 * secret, an `on conflict` with no predicate, a release identifier read before its insertion, and
 * a write order that made artifacts impossible to find. All observed on the cluster, none in a
 * test.
 *
 * # How to run it
 *
 * kubectl -n arkhitekton port-forward svc/arkhitekton-postgres 55432:5432 &
 * SCORE_LIVE=1 \
 * SCORE_DATABASE_URL='postgres://dashboard:…@localhost:55432/dashboard' \
 * SCORE_RELEASE_TAG=3.2.0-rc15 \
 *   go test -count=1 -run Live -v ./internal/http/
 *
 * **The database is not throwaway and is not erased**: this test only reads. That is the
 * difference with the repository's other live tests, which stand up their own schema.
 */
func TestLiveTheScoreOfARealRelease(t *testing.T) {
	if os.Getenv("SCORE_LIVE") != "1" {
		t.Skip("SCORE_LIVE not set: real check skipped")
	}
	url := os.Getenv("SCORE_DATABASE_URL")
	tag := os.Getenv("SCORE_RELEASE_TAG")
	if url == "" || tag == "" {
		t.Fatal("SCORE_DATABASE_URL and SCORE_RELEASE_TAG are required")
	}

	ctx := context.Background()
	db, err := store.Open(ctx, config.DatabaseConfig{DSN: url, MaxConns: 2})
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}
	defer db.Close()

	plugins, err := builtin.Registry()
	if err != nil {
		t.Fatalf("plugins: %v", err)
	}
	registry, err := catalog.Load("../../deploy/base/config/catalog.yaml", catalog.WithCapabilities(plugins))
	if err != nil {
		t.Fatalf("catalogue: %v", err)
	}

	// The project is the one the release names: these tests do not choose, they observe.
	release, err := releaseByTag(ctx, db, tag)
	if err != nil {
		t.Fatalf("release %q: %v", tag, err)
	}

	reports, err := db.ListReleaseTestResults(ctx, release.ID)
	if err != nil {
		t.Fatalf("results: %v", err)
	}

	breakdown, err := computeScore(ctx, db, release.ID, reports, registry)
	if err != nil {
		t.Fatalf("computing the score: %v", err)
	}

	t.Logf("release %s: score %d (measured: %v)", tag, breakdown.Score, breakdown.Measured)
	for _, component := range breakdown.Signals {
		t.Logf("  %-14s %-22s declared weight %5.1f  effective %5.1f  value %5.1f  %s",
			component.Key, component.Label,
			component.Declared, component.Weight, component.Value, component.Detail)
	}

	/*
	 * What this test **holds**, rather than what it observes.
	 */

	// The components come from the steps, not from the families. That is the change of axis: a
	// release that has a plan must no longer return a `signal:` component.
	for _, component := range breakdown.Signals {
		if component.Key == results.SignalKey(component.Signal) {
			t.Errorf("component %q identified by its signal — the fallback by kind "+
				"was applied to a release that nonetheless has a plan", component.Key)
		}
		// A component with no label does not display: the cell would be mute.
		if component.Label == "" {
			t.Errorf("component %q with no label", component.Key)
		}
	}

	// The effective weights of the measured components sum to 100. That is the renormalisation:
	// a 100 over two steps must not look like a 100 over six, and this split is what says so.
	var effective float64
	for _, component := range breakdown.Signals {
		if component.Measured {
			effective += component.Weight
		}
	}
	if breakdown.Measured && (effective < 99.5 || effective > 100.5) {
		t.Errorf("sum of the effective weights = %.2f, expected 100", effective)
	}

	// And every component removed from the computation is named: the reader must know what the
	// score they read covers.
	retired := 0
	for _, component := range breakdown.Signals {
		if !component.Measured {
			retired++
		}
	}
	if retired != len(breakdown.Redistributed) {
		t.Errorf("%d component(s) not measured and %d announced redistributed",
			retired, len(breakdown.Redistributed))
	}
}

// releaseByTag finds a release by its tag, whatever its project.
func releaseByTag(ctx context.Context, db *store.DB, tag string) (store.Release, error) {
	projects, err := db.ListProjects(ctx)
	if err != nil {
		return store.Release{}, err
	}
	for _, project := range projects {
		release, err := db.GetRelease(ctx, project.ID, tag)
		if err == nil {
			return release, nil
		}
	}
	return store.Release{}, store.ErrReleaseNotFound
}
