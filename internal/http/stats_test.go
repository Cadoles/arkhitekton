package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/release"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

type fakeStatsStore struct {
	releases  []store.ReleaseCount
	pipelines []store.PipelineStat
	// asked records the projects and the date each read was asked for.
	asked []int64
	since time.Time
}

func (f *fakeStatsStore) ListProjects(context.Context) ([]store.Project, error) {
	return []store.Project{
		{ID: 1, Slug: "shop", Label: "Shop", ReleaseKinds: release.Cycle{{ID: "pre", Label: "Pré-release"}}},
		{ID: 2, Slug: "theme", Label: "Thème"},
		{ID: 3, Slug: "tekton", Label: "Tekton"},
	}, nil
}

func (f *fakeStatsStore) ReleaseStats(_ context.Context, ids []int64, since time.Time, _ string) ([]store.ReleaseCount, error) {
	f.asked, f.since = ids, since
	return f.releases, nil
}

func (f *fakeStatsStore) PipelineStats(context.Context, []int64, time.Time) ([]store.PipelineStat, error) {
	return f.pipelines, nil
}

// thursday is 2026-11-19, a Thursday: its week starts on Monday the 16th.
var thursday = time.Date(2026, 11, 19, 15, 0, 0, 0, time.UTC)

type statsListing struct {
	Bucket   string    `json:"bucket"`
	Since    time.Time `json:"since"`
	Projects []struct {
		Slug string `json:"slug"`
	} `json:"projects"`
	Releases struct {
		Total   int          `json:"total"`
		ByKind  []statsCount `json:"by_kind"`
		ByState []statsCount `json:"by_state"`
		Buckets []struct {
			Start   time.Time      `json:"start"`
			Count   int            `json:"count"`
			ByState map[string]int `json:"by_state"`
		} `json:"buckets"`
	} `json:"releases"`
	Pipelines []statsPipeline `json:"pipelines"`
}

func statsCall(t *testing.T, fake *fakeStatsStore, identity auth.Identity, query string) (int, statsListing) {
	t.Helper()
	handler := NewStatsHandler(fake, &catalog.Catalog{Pipelines: []catalog.Pipeline{
		{Kind: "docker/build", Label: "Construction d'image"},
	}})
	handler.now = func() time.Time { return thursday }
	request := httptest.NewRequest(http.MethodGet, "/stats"+query, nil)
	request = request.WithContext(context.WithValue(request.Context(), identityKey, identity))
	recorder := httptest.NewRecorder()
	handler.List(recorder, request)
	var listing statsListing
	_ = json.Unmarshal(recorder.Body.Bytes(), &listing)
	return recorder.Code, listing
}

// "All my projects" counts only those the caller is an operator in; a named project they are not
// an operator in is refused.
func TestTheStatisticsCountOnlyTheCallersProjects(t *testing.T) {
	identity := auth.Identity{Subject: "s", Role: auth.RoleViewer,
		Projects: map[int64]auth.Role{1: auth.RoleOperator, 3: auth.RoleReleaser}}

	fake := &fakeStatsStore{}
	code, listing := statsCall(t, fake, identity, "")
	if code != http.StatusOK || !slices.Equal(fake.asked, []int64{1, 3}) || len(listing.Projects) != 2 {
		t.Fatalf("code %d, asked %v, projects %+v", code, fake.asked, listing.Projects)
	}

	if code, _ := statsCall(t, &fakeStatsStore{}, identity, "?project=theme"); code != http.StatusForbidden {
		t.Errorf("a project the caller only reads: %d", code)
	}
	fake = &fakeStatsStore{}
	if code, _ := statsCall(t, fake, identity, "?project=shop"); code != http.StatusOK || !slices.Equal(fake.asked, []int64{1}) {
		t.Errorf("one project: %d, asked %v", code, fake.asked)
	}
	if code, _ := statsCall(t, &fakeStatsStore{}, identity, "?range=1y"); code != http.StatusBadRequest {
		t.Errorf("unknown range: %d", code)
	}
}

// Every week of the period is there, empty ones included, and the counts land in theirs.
func TestTheReleasesAreCountedWeekByWeek(t *testing.T) {
	monday := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	fake := &fakeStatsStore{releases: []store.ReleaseCount{
		{Bucket: monday, Kind: "pre", State: "failed", Count: 2},
		{Bucket: monday, Kind: "pre", State: "released", Count: 1},
		{Bucket: monday.AddDate(0, 0, -14), Kind: "final", State: "released", Count: 1},
	}}
	_, listing := statsCall(t, fake, auth.Identity{Subject: "s", Role: auth.RoleOperator}, "?range=30d&project=shop")

	// 30 days back from Thursday the 19th is Monday 20 October's week.
	if listing.Bucket != "week" || !listing.Since.Equal(time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("bucket %s since %s", listing.Bucket, listing.Since)
	}
	buckets := listing.Releases.Buckets
	if len(buckets) != 5 || !buckets[4].Start.Equal(monday) {
		t.Fatalf("buckets %+v", buckets)
	}
	if buckets[4].Count != 3 || buckets[4].ByState["failed"] != 2 || buckets[2].Count != 1 || buckets[0].Count != 0 {
		t.Errorf("counts %+v", buckets)
	}
	if listing.Releases.Total != 4 || listing.Releases.ByKind[0].Key != "pre" ||
		listing.Releases.ByKind[0].Label != "Pré-release" || listing.Releases.ByKind[0].Count != 3 {
		t.Errorf("summary %+v", listing.Releases)
	}
}

// Twelve months, cut by month, and a pipeline named as the catalogue names it.
func TestThePipelinesAreNamedByTheCatalogue(t *testing.T) {
	median := 72.0
	fake := &fakeStatsStore{pipelines: []store.PipelineStat{
		{Kind: "docker/build", Runs: 10, Succeeded: 8, Failed: 2, MedianSeconds: &median},
		{Kind: "gone/kind", Runs: 1, Cancelled: 1},
	}}
	_, listing := statsCall(t, fake, auth.Identity{Subject: "s", Role: auth.RoleOperator}, "?range=12m")

	if listing.Bucket != "month" || len(listing.Releases.Buckets) != 12 ||
		!listing.Since.Equal(time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("bucket %s since %s, %d buckets", listing.Bucket, listing.Since, len(listing.Releases.Buckets))
	}
	if len(listing.Pipelines) != 2 || listing.Pipelines[0].Label != "Construction d'image" ||
		listing.Pipelines[1].Label != "gone/kind" || *listing.Pipelines[0].MedianSeconds != 72 {
		t.Errorf("pipelines %+v", listing.Pipelines)
	}
}
