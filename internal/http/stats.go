package http

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Cadoles/arkhitekton/internal/auth"
	"github.com/Cadoles/arkhitekton/internal/catalog"
	"github.com/Cadoles/arkhitekton/internal/store"
)

/*
 * The statistics: the releases launched and the pipelines run, over a period.
 *
 * Reserved to operators: the counts say how a project delivers, which is the concern of those who
 * launch its pipelines. A project counts only when the caller is an operator in it, so that the
 * sum over "all my projects" never includes one they cannot act on. The plugins bring no
 * indicator of their own: what they record (promotions, attestations) stays on their screens.
 */

// StatsStore is what the statistics need from the database.
type StatsStore interface {
	ListProjects(ctx context.Context) ([]store.Project, error)
	ReleaseStats(ctx context.Context, projectIDs []int64, since time.Time, bucket string) ([]store.ReleaseCount, error)
	PipelineStats(ctx context.Context, projectIDs []int64, since time.Time) ([]store.PipelineStat, error)
}

// StatsHandler serves the statistics.
type StatsHandler struct {
	store   StatsStore
	catalog catalog.Source
	now     func() time.Time
}

func NewStatsHandler(stats StatsStore, pipelines catalog.Source) *StatsHandler {
	return &StatsHandler{store: stats, catalog: pipelines, now: time.Now}
}

// statsRange is a period the screen offers: how far back, and how it is cut.
type statsRange struct {
	bucket string
	since  func(now time.Time) time.Time
}

// statsRanges are the periods offered. The start is a bucket's: a period cut by week starts on a
// Monday, so that its first bar counts a whole week.
var statsRanges = map[string]statsRange{
	"30d": {bucket: store.StatsWeek, since: func(now time.Time) time.Time { return weekStart(now.AddDate(0, 0, -30)) }},
	"90d": {bucket: store.StatsWeek, since: func(now time.Time) time.Time { return weekStart(now.AddDate(0, 0, -90)) }},
	"12m": {bucket: store.StatsMonth, since: func(now time.Time) time.Time { return monthStart(now.AddDate(0, -11, 0)) }},
}

func weekStart(at time.Time) time.Time {
	day := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
}

func monthStart(at time.Time) time.Time {
	return time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
}

type statsProject struct {
	Slug  string `json:"slug"`
	Label string `json:"label"`
}

type statsCount struct {
	Key   string `json:"key"`
	Label string `json:"label,omitempty"`
	Count int    `json:"count"`
}

type statsBucket struct {
	Start   time.Time      `json:"start"`
	Count   int            `json:"count"`
	ByState map[string]int `json:"by_state"`
}

type statsReleases struct {
	Total   int           `json:"total"`
	ByState []statsCount  `json:"by_state"`
	ByKind  []statsCount  `json:"by_kind"`
	Buckets []statsBucket `json:"buckets"`
}

type statsPipeline struct {
	Kind          string   `json:"kind"`
	Label         string   `json:"label"`
	Runs          int      `json:"runs"`
	Succeeded     int      `json:"succeeded"`
	Failed        int      `json:"failed"`
	Cancelled     int      `json:"cancelled"`
	MedianSeconds *float64 `json:"median_seconds,omitempty"`
}

// List returns the statistics of a project (`project`, a slug) or of every project the caller is
// an operator in, over a period (`range`: `30d`, `90d` or `12m`).
func (h *StatsHandler) List(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}
	rangeID := r.URL.Query().Get("range")
	if rangeID == "" {
		rangeID = "30d"
	}
	period, known := statsRanges[rangeID]
	if !known {
		writeProblem(w, http.StatusBadRequest, "Unknown range", "expected 30d, 90d or 12m")
		return
	}

	projects, err := h.store.ListProjects(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the projects", "")
		return
	}
	slug := strings.TrimSpace(r.URL.Query().Get("project"))
	var counted []store.Project
	for _, project := range projects {
		if slug != "" && project.Slug != slug {
			continue
		}
		if identity.RoleIn(project.ID).Allows(auth.RoleOperator) {
			counted = append(counted, project)
		}
	}
	if slug != "" && len(counted) == 0 {
		writeProblem(w, http.StatusForbidden, "Not allowed", "the statistics of a project are its operators'")
		return
	}

	now := h.now().UTC()
	since := period.since(now)
	ids := make([]int64, 0, len(counted))
	scope := make([]statsProject, 0, len(counted))
	for _, project := range counted {
		ids = append(ids, project.ID)
		scope = append(scope, statsProject{Slug: project.Slug, Label: project.Label})
	}

	releases, err := h.releases(r.Context(), counted, ids, since, now, period.bucket)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot count the releases", "")
		return
	}
	pipelines, err := h.pipelines(r.Context(), ids, since)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot count the pipelines", "")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"range":     rangeID,
		"bucket":    period.bucket,
		"since":     since,
		"projects":  scope,
		"releases":  releases,
		"pipelines": pipelines,
	})
}

// releases sums the counts up, with every bucket of the period present, empty ones included: a
// chart with gaps would read as a period that does not exist.
func (h *StatsHandler) releases(
	ctx context.Context, projects []store.Project, ids []int64, since, now time.Time, bucket string,
) (statsReleases, error) {
	summary := statsReleases{ByState: []statsCount{}, ByKind: []statsCount{}, Buckets: []statsBucket{}}
	for start := since; !start.After(now); start = nextBucket(start, bucket) {
		summary.Buckets = append(summary.Buckets, statsBucket{Start: start, ByState: map[string]int{}})
	}
	if len(ids) == 0 {
		return summary, nil
	}
	counts, err := h.store.ReleaseStats(ctx, ids, since, bucket)
	if err != nil {
		return summary, err
	}

	// A kind is named by the first cycle that declares it: two projects rarely give the same id two
	// different names.
	kindLabels := map[string]string{}
	for _, project := range projects {
		for _, kind := range project.ReleaseKinds {
			if _, named := kindLabels[kind.ID]; !named {
				kindLabels[kind.ID] = kind.Label
			}
		}
	}

	byState, byKind := map[string]int{}, map[string]int{}
	for _, count := range counts {
		summary.Total += count.Count
		byState[count.State] += count.Count
		byKind[count.Kind] += count.Count
		for index := range summary.Buckets {
			if summary.Buckets[index].Start.Equal(count.Bucket) {
				summary.Buckets[index].Count += count.Count
				summary.Buckets[index].ByState[count.State] += count.Count
			}
		}
	}
	summary.ByState = sortedCounts(byState, nil)
	summary.ByKind = sortedCounts(byKind, kindLabels)
	return summary, nil
}

func nextBucket(start time.Time, bucket string) time.Time {
	if bucket == store.StatsMonth {
		return start.AddDate(0, 1, 0)
	}
	return start.AddDate(0, 0, 7)
}

func sortedCounts(counts map[string]int, labels map[string]string) []statsCount {
	sorted := make([]statsCount, 0, len(counts))
	for key, count := range counts {
		sorted = append(sorted, statsCount{Key: key, Label: labels[key], Count: count})
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Count != sorted[j].Count {
			return sorted[i].Count > sorted[j].Count
		}
		return sorted[i].Key < sorted[j].Key
	})
	return sorted
}

// pipelines names each kind as the catalogue does; a kind it no longer carries keeps its id.
func (h *StatsHandler) pipelines(ctx context.Context, ids []int64, since time.Time) ([]statsPipeline, error) {
	views := []statsPipeline{}
	if len(ids) == 0 {
		return views, nil
	}
	stats, err := h.store.PipelineStats(ctx, ids, since)
	if err != nil {
		return nil, err
	}
	for _, stat := range stats {
		label := stat.Kind
		if pipeline, found := catalog.Of(h.catalog).Find(stat.Kind); found {
			label = pipelineLabel(pipeline)
		}
		views = append(views, statsPipeline{
			Kind: stat.Kind, Label: label, Runs: stat.Runs, Succeeded: stat.Succeeded,
			Failed: stat.Failed, Cancelled: stat.Cancelled, MedianSeconds: stat.MedianSeconds,
		})
	}
	return views, nil
}
