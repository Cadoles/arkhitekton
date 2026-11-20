package store

import (
	"context"
	"fmt"
	"time"
)

/*
 * The statistics: counts over the releases and the jobs of some projects, since a date.
 *
 * Computed at each read and not kept as aggregates: the indexes on (project, date) keep a
 * period's scan to that period's rows, however long the history grows.
 */

// Stats buckets.
const (
	StatsWeek  = "week"
	StatsMonth = "month"
)

// ReleaseCount is how many releases of a kind reached a state, in a bucket.
type ReleaseCount struct {
	// Bucket is the start of the week (Monday) or month, in UTC.
	Bucket time.Time
	Kind   string
	State  string
	Count  int
}

// ReleaseStats counts the releases launched since a date, by bucket, kind and state. A draft is not
// counted: it has not been launched.
func (db *DB) ReleaseStats(ctx context.Context, projectIDs []int64, since time.Time, bucket string) ([]ReleaseCount, error) {
	if bucket != StatsWeek && bucket != StatsMonth {
		return nil, fmt.Errorf("unknown bucket %q", bucket)
	}
	rows, err := db.pool.Query(ctx,
		`select date_trunc($3, created_at at time zone 'UTC'), kind, state, count(*)
		 from releases
		 where project_id = any($1) and created_at >= $2 and state <> 'draft'
		 group by 1, 2, 3
		 order by 1, 2, 3`, projectIDs, since, bucket)
	if err != nil {
		return nil, fmt.Errorf("release statistics: %w", err)
	}
	defer rows.Close()

	var counts []ReleaseCount
	for rows.Next() {
		var count ReleaseCount
		if err := rows.Scan(&count.Bucket, &count.Kind, &count.State, &count.Count); err != nil {
			return nil, fmt.Errorf("release statistics: %w", err)
		}
		count.Bucket = count.Bucket.UTC()
		counts = append(counts, count)
	}
	return counts, rows.Err()
}

// PipelineStat is what the jobs of a kind did since a date.
type PipelineStat struct {
	Kind      string
	Runs      int
	Succeeded int
	Failed    int
	Cancelled int
	// MedianSeconds is the median duration of the runs that started, nil when none did.
	MedianSeconds *float64
}

// PipelineStats sums up, by kind, the pipelines of some projects that finished since a date. A
// pipeline's kind is qualified by the plugin that brings it (`docker/build`); the core's local tasks
// (`release-notes`, `release-plan`) are not, and are not counted.
func (db *DB) PipelineStats(ctx context.Context, projectIDs []int64, since time.Time) ([]PipelineStat, error) {
	rows, err := db.pool.Query(ctx,
		`select kind, count(*),
		        count(*) filter (where state = 'succeeded'),
		        count(*) filter (where state = 'failed'),
		        count(*) filter (where state = 'cancelled'),
		        percentile_cont(0.5) within group (order by extract(epoch from finished_at - started_at))
		          filter (where started_at is not null)
		 from jobs
		 where project_id = any($1) and finished_at >= $2
		   and state in ('succeeded', 'failed', 'cancelled') and kind like '%/%'
		 group by kind
		 order by count(*) desc, kind`, projectIDs, since)
	if err != nil {
		return nil, fmt.Errorf("pipeline statistics: %w", err)
	}
	defer rows.Close()

	var stats []PipelineStat
	for rows.Next() {
		var stat PipelineStat
		if err := rows.Scan(&stat.Kind, &stat.Runs, &stat.Succeeded, &stat.Failed, &stat.Cancelled,
			&stat.MedianSeconds); err != nil {
			return nil, fmt.Errorf("pipeline statistics: %w", err)
		}
		stats = append(stats, stat)
	}
	return stats, rows.Err()
}
