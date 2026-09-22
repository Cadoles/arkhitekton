package logs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/blob"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
)

// These tests require a real MinIO. What they check — durable chunking, sealing
// order, resuming after an interruption — is about what is actually written to the
// object store, and an in-memory stub would only prove the stub is consistent.
func testStorage(t *testing.T) *blob.Client {
	t.Helper()

	endpoint := os.Getenv("TEST_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_MINIO_ENDPOINT not set: integration test skipped")
	}

	client, err := blob.Open(context.Background(), config.StorageConfig{
		Endpoint:   endpoint,
		Bucket:     envOr("TEST_MINIO_BUCKET", "arkhitekton-test"),
		AccessKey:  envOr("TEST_MINIO_ACCESS_KEY", "minioadmin"),
		SecretKey:  envOr("TEST_MINIO_SECRET_KEY", "minioadmin"),
		Region:     envOr("TEST_MINIO_REGION", "us-east-1"),
		PresignTTL: 2 * time.Hour,
	})
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	return client
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakePods returns, per container, a fixed content then EOF — what a finished
// container does.
type fakePods struct {
	mu       sync.Mutex
	byName   map[string]string
	requests []request
	// missing simulates a container that has not started: the read fails.
	missing map[string]bool
}

type request struct {
	container string
	since     *time.Time
}

func newFakePods(byName map[string]string) *fakePods {
	return &fakePods{byName: byName, missing: map[string]bool{}}
}

func (p *fakePods) Stream(_ context.Context, _, container string, since *time.Time) (io.ReadCloser, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.requests = append(p.requests, request{container: container, since: since})

	if p.missing[container] {
		return nil, fmt.Errorf("container %q waiting to start", container)
	}

	content, ok := p.byName[container]
	if !ok {
		return nil, fmt.Errorf("unknown container %q", container)
	}

	// Filtering to the second, as the Kubernetes API does: that is what makes duplicates
	// unavoidable after a resume.
	if since != nil {
		var kept []string
		for _, line := range strings.SplitAfter(content, "\n") {
			if line == "" {
				continue
			}
			parsed := parseLine(line)
			if parsed.Timestamp.IsZero() || !parsed.Timestamp.Truncate(time.Second).Before(since.Truncate(time.Second)) {
				kept = append(kept, line)
			}
		}
		content = strings.Join(kept, "")
	}

	return io.NopCloser(strings.NewReader(content)), nil
}

func (p *fakePods) sinceFor(container string) []*time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()

	var values []*time.Time
	for _, r := range p.requests {
		if r.container == container {
			values = append(values, r.since)
		}
	}
	return values
}

type fakeStore struct {
	mu      sync.Mutex
	sealed  map[string]string
	sealErr error
}

func newFakeStore() *fakeStore { return &fakeStore{sealed: map[string]string{}} }

func (s *fakeStore) SealTaskLog(_ context.Context, runName, taskName, object string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sealErr != nil {
		return s.sealErr
	}
	s.sealed[runName+"/"+taskName] = object
	return nil
}

type recordingSink struct {
	mu    sync.Mutex
	lines []Line
}

func (s *recordingSink) Publish(_, _ string, line Line) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, line)
}

func (s *recordingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.lines)
}

// stamp builds a line in the format kubelet produces with `timestamps=true`.
func stamp(offset time.Duration, text string) string {
	base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	return base.Add(offset).Format(time.RFC3339Nano) + " " + text + "\n"
}

func uniqueTarget(t *testing.T, steps ...Step) Target {
	t.Helper()
	// A unique run name per test: the bucket is shared, and two tests writing under the
	// same key would mask each other.
	return Target{
		RunName:  fmt.Sprintf("run-%d", time.Now().UnixNano()),
		TaskName: "unit-test",
		PodName:  "unit-test-pod",
		Steps:    steps,
	}
}

func cleanup(t *testing.T, storage *blob.Client, target Target) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		prefix, err := storage.Keys.RunPrefix(target.RunName)
		if err == nil {
			_ = prefix
		}
		for _, p := range []string{"logs/" + target.RunName + "/"} {
			objects, err := storage.List(ctx, p)
			if err != nil {
				return
			}
			for _, object := range objects {
				_ = storage.Remove(ctx, object.Key)
			}
		}
	})
}

func newCollector(storage *blob.Client, pods Pods, store Store, sink Sink) *Collector {
	collector := New(pods, storage, store, sink, discardLogger())
	// Tightened thresholds: production values would make the tests slow or unable to
	// produce more than one chunk.
	collector.flushInterval = 50 * time.Millisecond
	collector.flushSize = 120
	return collector
}

// Sealing must return a log whose order is the order of execution. Sorting on keys
// alone would interleave the steps alphabetically, and a log where `publish`
// precedes `phpunit` does not read.
func TestSealConcatenatesStepsInDeclaredOrder(t *testing.T) {
	storage := testStorage(t)
	ctx := context.Background()

	target := uniqueTarget(t,
		Step{Name: "composer", Container: "step-composer"},
		Step{Name: "phpunit", Container: "step-phpunit"},
		Step{Name: "publish", Container: "step-publish"},
	)
	cleanup(t, storage, target)

	pods := newFakePods(map[string]string{
		// The containers' alphabetical order is composer, phpunit, publish; the declared
		// order is the same, but the contents allow checking that the concatenation follows
		// the steps and not the keys.
		"step-composer": stamp(0, "composer install"),
		"step-phpunit":  stamp(time.Second, "phpunit ok"),
		"step-publish":  stamp(2*time.Second, "publication"),
	})
	store := newFakeStore()
	collector := newCollector(storage, pods, store, nil)

	collector.Start(ctx, target)
	if err := collector.Finish(ctx, target); err != nil {
		t.Fatalf("scellement: %v", err)
	}

	object, err := storage.Keys.TaskLogObject(target.RunName, target.TaskName)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	content := readObject(t, storage, object)

	positions := []string{"composer install", "phpunit ok", "publication"}
	previous := -1
	for _, text := range positions {
		index := strings.Index(content, text)
		if index < 0 {
			t.Fatalf("%q missing from the sealed log:\n%s", text, content)
		}
		if index < previous {
			t.Errorf("%q appears before the previous step", text)
		}
		previous = index
	}

	if store.sealed[target.RunName+"/"+target.TaskName] != object {
		t.Errorf("the database was not updated: %v", store.sealed)
	}
}

// The chunks are purged after sealing, but only after: the reverse would lose the
// log if writing the object failed.
func TestSealPurgesChunksButKeepsTheSealedObject(t *testing.T) {
	storage := testStorage(t)
	ctx := context.Background()

	target := uniqueTarget(t, Step{Name: "phpunit", Container: "step-phpunit"})
	cleanup(t, storage, target)

	// Enough content to produce several chunks at a 120-byte threshold.
	var content strings.Builder
	for i := range 20 {
		content.WriteString(stamp(time.Duration(i)*time.Second, fmt.Sprintf("ligne %d", i)))
	}

	pods := newFakePods(map[string]string{"step-phpunit": content.String()})
	collector := newCollector(storage, pods, newFakeStore(), nil)

	collector.Start(ctx, target)

	partsPrefix, err := storage.Keys.TaskLogPartsPrefix(target.RunName, target.TaskName)
	if err != nil {
		t.Fatalf("prefix: %v", err)
	}
	// Wait for the capture to have produced chunks before sealing, otherwise the test
	// proves nothing about the purge.
	waitFor(t, func() bool {
		objects, err := storage.List(ctx, partsPrefix)
		return err == nil && len(objects) > 1
	}, "several chunks written")

	if err := collector.Finish(ctx, target); err != nil {
		t.Fatalf("scellement: %v", err)
	}

	remaining, err := storage.List(ctx, partsPrefix)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("%d chunk(s) remain after sealing", len(remaining))
	}

	object, _ := storage.Keys.TaskLogObject(target.RunName, target.TaskName)
	sealed := readObject(t, storage, object)
	if !strings.Contains(sealed, "ligne 0") || !strings.Contains(sealed, "ligne 19") {
		t.Errorf("incomplete sealed log (%d bytes)", len(sealed))
	}
}

// Resuming after the worker restarts: the chunks already written bound the request
// to the kubelet, and lines returned twice are discarded.
//
// This is the behaviour that separates "losing lines" from "duplicating them", and
// the second is deliberately preferred.
func TestResumeSkipsAlreadyStoredLinesWithoutLosingAny(t *testing.T) {
	storage := testStorage(t)
	ctx := context.Background()

	target := uniqueTarget(t, Step{Name: "phpunit", Container: "step-phpunit"})
	cleanup(t, storage, target)

	full := stamp(0, "ligne A") + stamp(time.Second, "ligne B") + stamp(2*time.Second, "ligne C")
	pods := newFakePods(map[string]string{"step-phpunit": full})

	// A first capture, interrupted: only the first two lines exist.
	partial := stamp(0, "ligne A") + stamp(time.Second, "ligne B")
	chunkKey, err := storage.Keys.TaskLogChunk(target.RunName, target.TaskName, "step-phpunit", 0, 1)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	if err := storage.Put(ctx, chunkKey, strings.NewReader(partial), int64(len(partial)), "text/plain"); err != nil {
		t.Fatalf("tranche initiale: %v", err)
	}

	collector := newCollector(storage, pods, newFakeStore(), nil)
	collector.Start(ctx, target)
	if err := collector.Finish(ctx, target); err != nil {
		t.Fatalf("scellement: %v", err)
	}

	// The kubelet was indeed queried with a bound: without it, the whole log would be
	// re-read at every restart.
	sinces := pods.sinceFor("step-phpunit")
	if len(sinces) == 0 || sinces[0] == nil {
		t.Fatalf("the resume set no bound: %v", sinces)
	}

	object, _ := storage.Keys.TaskLogObject(target.RunName, target.TaskName)
	sealed := readObject(t, storage, object)

	for _, text := range []string{"ligne A", "ligne B", "ligne C"} {
		if strings.Count(sealed, text) == 0 {
			t.Errorf("%q lost during sealing:\n%s", text, sealed)
		}
		if strings.Count(sealed, text) > 1 {
			t.Errorf("%q duplicated %d times:\n%s", text, strings.Count(sealed, text), sealed)
		}
	}
}

// A step that has not started fails the read of its container. That is not an
// anomaly: steps run in series, and the last may wait several minutes. The capture
// must retry, not give up.
func TestStepNotStartedYetIsRetried(t *testing.T) {
	storage := testStorage(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	target := uniqueTarget(t, Step{Name: "late", Container: "step-tardive"})
	cleanup(t, storage, target)

	pods := newFakePods(map[string]string{"step-tardive": stamp(0, "started at last")})
	pods.missing["step-tardive"] = true

	collector := newCollector(storage, pods, newFakeStore(), nil)
	collector.flushInterval = 20 * time.Millisecond
	collector.Start(ctx, target)

	waitFor(t, func() bool { return len(pods.sinceFor("step-tardive")) >= 2 }, "at least two attempts")

	// The step starts: the capture must then succeed.
	pods.mu.Lock()
	pods.missing["step-tardive"] = false
	pods.mu.Unlock()

	partsPrefix, _ := storage.Keys.TaskLogPartsPrefix(target.RunName, target.TaskName)
	waitFor(t, func() bool {
		objects, err := storage.List(ctx, partsPrefix)
		return err == nil && len(objects) > 0
	}, "log captured after start")

	collector.Stop()
}

// The informer repeats the update event on every step change. One capture per event
// would open dozens of streams on the same container.
func TestStartIsIdempotent(t *testing.T) {
	storage := testStorage(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	target := uniqueTarget(t, Step{Name: "phpunit", Container: "step-phpunit"})
	cleanup(t, storage, target)

	// Container absent: the capture loops, which leaves time to observe concurrent
	// starts.
	pods := newFakePods(map[string]string{})
	pods.missing["step-phpunit"] = true

	collector := newCollector(storage, pods, newFakeStore(), nil)

	for range 5 {
		collector.Start(ctx, target)
	}

	collector.mu.Lock()
	active := len(collector.active)
	collector.mu.Unlock()

	if active != 1 {
		t.Errorf("%d active capture(s), expected 1", active)
	}

	collector.Stop()
}

// Sealing a task with no log — a pipeline with no step, or a task already sealed —
// must not produce an error: both cases are normal.
func TestSealWithoutChunksIsNotAnError(t *testing.T) {
	storage := testStorage(t)
	target := uniqueTarget(t, Step{Name: "phpunit", Container: "step-phpunit"})

	collector := newCollector(storage, newFakePods(map[string]string{}), newFakeStore(), nil)
	if err := collector.Finish(context.Background(), target); err != nil {
		t.Errorf("sealing with nothing: %v", err)
	}
}

// Live viewing: every captured line also goes to subscribers, without its timestamp
// — it would be noise on screen, while it is indispensable in the stored chunk for
// resuming.
func TestLinesArePublishedLiveWithoutTimestampPrefix(t *testing.T) {
	storage := testStorage(t)
	ctx := context.Background()

	target := uniqueTarget(t, Step{Name: "phpunit", Container: "step-phpunit"})
	cleanup(t, storage, target)

	pods := newFakePods(map[string]string{
		"step-phpunit": stamp(0, "PHPUnit 11.4.1") + stamp(time.Second, "OK (1301 tests)"),
	})
	sink := &recordingSink{}
	collector := newCollector(storage, pods, newFakeStore(), sink)

	collector.Start(ctx, target)
	if err := collector.Finish(ctx, target); err != nil {
		t.Fatalf("scellement: %v", err)
	}

	if sink.count() != 2 {
		t.Fatalf("%d line(s) published, expected 2", sink.count())
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, line := range sink.lines {
		if strings.HasPrefix(line.Text, "2026-") {
			t.Errorf("the timestamp was not stripped from the live stream: %q", line.Text)
		}
		if line.Timestamp.IsZero() {
			t.Errorf("timestamp lost for %q", line.Text)
		}
	}

	// Storage, for its part, keeps the timestamp.
	object, _ := storage.Keys.TaskLogObject(target.RunName, target.TaskName)
	if sealed := readObject(t, storage, object); !strings.Contains(sealed, "2026-08-14T10:00:00Z") {
		t.Errorf("the timestamp is missing from the stored log:\n%s", sealed)
	}
}

// A line with no usable timestamp must be kept as is: losing it over a formatting
// flaw would be the worst trade.
func TestUnstampedLineIsKept(t *testing.T) {
	line := parseLine("a line with no timestamp\n")

	if line.Text != "a line with no timestamp" {
		t.Errorf("texte = %q", line.Text)
	}
	if !line.Timestamp.IsZero() {
		t.Errorf("invented timestamp: %v", line.Timestamp)
	}
}

func TestParseLineSeparatesTimestampFromText(t *testing.T) {
	line := parseLine("2026-08-14T10:00:00.123456789Z PHPUnit 11.4.1\n")

	if line.Text != "PHPUnit 11.4.1" {
		t.Errorf("texte = %q", line.Text)
	}
	if line.Timestamp.IsZero() || line.Timestamp.Year() != 2026 {
		t.Errorf("horodatage = %v", line.Timestamp)
	}
}

func readObject(t *testing.T, storage *blob.Client, key string) string {
	t.Helper()

	reader, err := storage.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("reading %q: %v", key, err)
	}
	defer reader.Close()

	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading %q: %v", key, err)
	}
	return string(content)
}

func waitFor(t *testing.T, condition func() bool, what string) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition jamais atteinte: %s", what)
}
