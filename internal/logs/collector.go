// Package logs captures Tekton pod logs before they disappear.
//
// Tekton logs are pod logs: they vanish with the pod, and a task's pod is deleted
// shortly after it ends. Reading them on demand therefore works for a few minutes
// only. They must be captured while the task runs.
//
// Two consequences imposed by S3:
//
//  1. No multipart upload. A multipart part must weigh at least 5 MiB, which rules
//     out a useful flush granularity — an incident would lose up to 5 MiB of log.
//     Each flush therefore writes a complete object, durable immediately, and
//     sealing concatenates them.
//  2. One goroutine per step rather than a single stream. A TaskRun's steps start
//     one after another, and a step that has not started fails the read of its
//     container. Each step writes into its own chunk space, and the order is
//     restored at sealing — it is known in advance.
package logs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/blob"
)

// Flush defaults.
const (
	DefaultFlushInterval = 5 * time.Second
	DefaultFlushSize     = 1 << 20 // 1 Mio
)

// retryDelay separates two attempts to read a container that has not started.
const retryDelay = 2 * time.Second

// drainGrace bounds how long sealing waits for reads in flight to reach the end of
// the stream.
//
// The delay exists because a TaskRun becomes terminal before the log stream is
// exhausted client-side: cutting at that instant would truncate the end of the log,
// which is precisely the failure message one came for.
const drainGrace = 30 * time.Second

// Pods reads logs. The interface exists so the collector is testable without a
// cluster: what is worth exercising — chunking, resuming, sealing — has nothing to
// do with Kubernetes.
type Pods interface {
	// Stream opens a container's log. A non-nil `since` resumes from that instant. The
	// caller closes it.
	Stream(ctx context.Context, pod, container string, since *time.Time) (io.ReadCloser, error)
}

// Sink receives lines as they come, for live viewing.
//
// No persistence at this level: a client connecting mid-run first receives the
// chunks already written, then this stream.
type Sink interface {
	Publish(runName, taskName string, line Line)
}

// Line is a log line timestamped by kubelet.
type Line struct {
	Timestamp time.Time
	Text      string
}

// Store is what the collector writes to the database at the end.
type Store interface {
	SealTaskLog(ctx context.Context, runName, taskName, object string) error
}

// Step names a step and its container.
type Step struct {
	Name      string
	Container string
}

// Target describes the task to follow.
type Target struct {
	RunName  string
	TaskName string
	PodName  string
	Steps    []Step
}

func (t Target) key() string { return t.RunName + "/" + t.TaskName }

// Collector follows running tasks and seals their logs.
type Collector struct {
	pods    Pods
	storage *blob.Client
	store   Store
	live    Sink
	logger  *slog.Logger

	flushInterval time.Duration
	flushSize     int

	mu     sync.Mutex
	active map[string]*collection
}

type collection struct {
	cancel context.CancelFunc
	// drain is closed to say "no more attempts". Reads in flight carry on to the end of
	// the stream: that is the difference between a complete log and one truncated at the
	// last second.
	drain chan struct{}
	done  chan struct{}
}

// New assembles the collector.
func New(pods Pods, storage *blob.Client, store Store, live Sink, logger *slog.Logger) *Collector {
	if live == nil {
		live = discardSink{}
	}
	return &Collector{
		pods:          pods,
		storage:       storage,
		store:         store,
		live:          live,
		logger:        logger,
		flushInterval: DefaultFlushInterval,
		flushSize:     DefaultFlushSize,
		active:        make(map[string]*collection),
	}
}

// Start begins capturing a task, unless it is already under way.
//
// Idempotence is not a refinement: the informer repeats the update event on every
// step status change, and one capture per event would open dozens of streams on the
// same container.
func (c *Collector) Start(ctx context.Context, target Target) {
	if target.PodName == "" || len(target.Steps) == 0 {
		// With no pod there is nothing to read. The next event will bring one.
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, running := c.active[target.key()]; running {
		return
	}

	// A context detached from the event's: the capture outlives the event that started
	// it, and stops only with the collector or the task.
	captureCtx, cancel := context.WithCancel(ctx)
	entry := &collection{cancel: cancel, drain: make(chan struct{}), done: make(chan struct{})}
	c.active[target.key()] = entry

	c.logger.Info("log capture started",
		"run", target.RunName, "task", target.TaskName, "pod", target.PodName, "steps", len(target.Steps))

	go func() {
		defer close(entry.done)
		c.capture(captureCtx, entry.drain, target)
	}()
}

// Finish stops capturing a task and seals its log.
//
// Called when the TaskRun becomes terminal. Sealing waits for the goroutines to
// finish: sealing while a step is still writing would produce a truncated log, and
// that is irreversible since the chunks are purged afterwards.
func (c *Collector) Finish(ctx context.Context, target Target) error {
	c.mu.Lock()
	entry, running := c.active[target.key()]
	if running {
		delete(c.active, target.key())
	}
	c.mu.Unlock()

	if running {
		// Reads in flight run to the end of the stream; only new attempts stop. With the
		// container finished, EOF arrives within the second — the grace period covers the
		// case where the stream lags.
		close(entry.drain)

		select {
		case <-entry.done:
		case <-time.After(drainGrace):
			c.logger.Warn("grace period exceeded, capture interrupted",
				"run", target.RunName, "task", target.TaskName)
			entry.cancel()
			<-entry.done
		case <-ctx.Done():
			entry.cancel()
			return ctx.Err()
		}
	}

	return c.seal(ctx, target)
}

// Stop interrupts every capture without sealing: this is the worker stopping, not
// the tasks ending. The chunks already written stay, and a resume will build on
// them.
func (c *Collector) Stop() {
	c.mu.Lock()
	entries := make([]*collection, 0, len(c.active))
	for key, entry := range c.active {
		entries = append(entries, entry)
		delete(c.active, key)
	}
	c.mu.Unlock()

	for _, entry := range entries {
		entry.cancel()
	}
	for _, entry := range entries {
		<-entry.done
	}
}

func (c *Collector) capture(ctx context.Context, drain <-chan struct{}, target Target) {
	var wg sync.WaitGroup
	for index, step := range target.Steps {
		wg.Add(1)
		go func(index int, step Step) {
			defer wg.Done()
			c.captureStep(ctx, drain, target, step, index)
		}(index, step)
	}
	wg.Wait()
}

// captureStep follows a step until its container ends.
func (c *Collector) captureStep(
	ctx context.Context, drain <-chan struct{}, target Target, step Step, index int,
) {
	writer, err := newChunkWriter(ctx, c.storage, target, step, index, c.flushSize)
	if err != nil {
		c.logger.Error("preparing the chunks", "task", target.TaskName, "step", step.Name, "error", err)
		return
	}

	// Resuming: the last timestamp already written bounds both the request to the
	// kubelet and the local filtering. `sinceTime` has second precision, so it returns
	// lines already known — better to duplicate some than to lose any, and the filter
	// discards them.
	since := writer.lastTimestamp
	if since != nil {
		c.logger.Info("capture resumed",
			"task", target.TaskName, "step", step.Name, "since", since.Format(time.RFC3339))
	}

	for {
		if ctx.Err() != nil {
			c.flushQuietly(ctx, writer, target, step)
			return
		}

		stream, err := c.pods.Stream(ctx, target.PodName, step.Container, since)
		if err != nil {
			// The common case, not an anomaly: the step has not started. A TaskRun's steps run
			// in series, and the last one may wait several minutes.
			if ctx.Err() != nil {
				c.flushQuietly(ctx, writer, target, step)
				return
			}
			select {
			case <-ctx.Done():
				c.flushQuietly(ctx, writer, target, step)
				return
			case <-drain:
				// The task is over and this container never produced anything — a step Tekton
				// skipped, or a pod already deleted. Insisting would achieve nothing.
				c.flushQuietly(ctx, writer, target, step)
				return
			case <-time.After(retryDelay):
				continue
			}
		}

		lastRead, err := c.consume(ctx, stream, writer, target, step)
		_ = stream.Close()

		if lastRead != nil {
			since = lastRead
		}

		// EOF means the container finished: the log is complete. Any other error — a cut
		// stream, a lost connection — justifies resuming.
		if err == nil || errors.Is(err, io.EOF) {
			c.flushQuietly(ctx, writer, target, step)
			return
		}
		if ctx.Err() != nil {
			c.flushQuietly(ctx, writer, target, step)
			return
		}

		c.logger.Warn("log stream interrupted, resuming",
			"task", target.TaskName, "step", step.Name, "error", err)
		if err := writer.flush(ctx); err != nil {
			c.logger.Error("writing a chunk", "task", target.TaskName, "error", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-drain:
			return
		case <-time.After(retryDelay):
		}
	}
}

// consume reads the stream line by line and returns the last timestamp seen.
func (c *Collector) consume(
	ctx context.Context, stream io.Reader, writer *chunkWriter, target Target, step Step,
) (*time.Time, error) {
	reader := bufio.NewReaderSize(stream, 64<<10)
	ticker := time.NewTicker(c.flushInterval)
	defer ticker.Stop()

	// The periodic flush is carried by a goroutine: reading blocks, and a pipeline
	// silent for two minutes must not delay the durability of what came before by as
	// much.
	flushDone := make(chan struct{})
	defer func() { close(flushDone) }()
	go func() {
		for {
			select {
			case <-flushDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := writer.flush(ctx); err != nil {
					c.logger.Error("periodic chunk write",
						"task", target.TaskName, "step", step.Name, "error", err)
				}
			}
		}
	}()

	var last *time.Time
	for {
		raw, err := reader.ReadString('\n')
		if raw != "" {
			line := parseLine(raw)
			if !writer.accept(line) {
				// A line already known, returned by `sinceTime`: discarded quietly, the price of
				// losing nothing.
				continue
			}
			timestamp := line.Timestamp
			last = &timestamp

			if err := writer.append(ctx, raw); err != nil {
				return last, err
			}
			c.live.Publish(target.RunName, target.TaskName, line)
		}
		if err != nil {
			return last, err
		}
	}
}

func (c *Collector) flushQuietly(ctx context.Context, writer *chunkWriter, target Target, step Step) {
	// A fresh context: the capture's may already be cancelled, and the last chunk is
	// precisely the one not to lose.
	flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	if err := writer.flush(flushCtx); err != nil {
		c.logger.Error("writing the last chunk",
			"task", target.TaskName, "step", step.Name, "error", err)
	}
}

// Plain returns the text of a stored log, stripped of its timestamps.
//
// Stored chunks keep the RFC3339Nano prefix kubelet adds: it is what makes resuming
// possible after the collector restarts. Any reader after something other than
// display must therefore strip it first.
//
// Written here and not in the caller because the format belongs to this package.
// Forgetting it breaks nothing visible: the lines read perfectly, and only a search
// anchored at the start of a line fails — silently. That is what happened to the
// marker channel, whose descriptors were each preceded by a timestamp and therefore
// never recognised.
func Plain(stored string) string {
	lines := strings.Split(stored, "\n")
	for index, line := range lines {
		lines[index] = parseLine(line).Text
	}
	return strings.Join(lines, "\n")
}

// parseLine separates the timestamp kubelet adds from the text.
//
// `timestamps=true` prefixes every line with an RFC3339Nano followed by a space. The
// prefix is kept in the stored chunk — it is what makes resuming possible — and
// stripped for live viewing, where it would be noise.
func parseLine(raw string) Line {
	trimmed := strings.TrimRight(raw, "\r\n")

	prefix, text, found := strings.Cut(trimmed, " ")
	if !found {
		return Line{Text: trimmed}
	}
	timestamp, err := time.Parse(time.RFC3339Nano, prefix)
	if err != nil {
		// A line with no usable timestamp: kept as is. Losing a line because it is malformed
		// would be the worst trade.
		return Line{Text: trimmed}
	}
	return Line{Timestamp: timestamp, Text: text}
}

type discardSink struct{}

func (discardSink) Publish(string, string, Line) {}

// seal concatenates a task's chunks into a single object, then purges them.
//
// The single object is what makes ranged reads possible: the interface displays only
// the end of a log tens of megabytes long and goes back on demand. Doing that over a
// collection of chunks would mean listing them and computing offsets on every
// request.
func (c *Collector) seal(ctx context.Context, target Target) error {
	partsPrefix, err := c.storage.Keys.TaskLogPartsPrefix(target.RunName, target.TaskName)
	if err != nil {
		return err
	}

	chunks, err := c.storage.List(ctx, partsPrefix)
	if err != nil {
		return err
	}
	if len(chunks) == 0 {
		// Nothing was captured: a task with no log, or one already sealed. Both are normal
		// and do not justify an error.
		return nil
	}

	object, err := c.storage.Keys.TaskLogObject(target.RunName, target.TaskName)
	if err != nil {
		return err
	}

	// Keys carry the step's rank then the chunk's, both zero-padded: a lexicographic
	// sort is enough to restore chronology. Nothing about the pipeline need be known to
	// assemble its log.
	ordered := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		ordered = append(ordered, chunk.Key)
	}
	sortStrings(ordered)

	// Assembled in a buffer then written as a single object. Piping into PutObject would
	// avoid the buffer but hide the size — and therefore the ability to log and check
	// it.
	var assembled bytes.Buffer
	for _, key := range ordered {
		reader, err := c.storage.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("reading chunk %q: %w", key, err)
		}
		if _, err := io.Copy(&assembled, reader); err != nil {
			_ = reader.Close()
			return fmt.Errorf("assembling chunk %q: %w", key, err)
		}
		_ = reader.Close()
	}

	if err := c.storage.Put(ctx, object,
		bytes.NewReader(assembled.Bytes()), int64(assembled.Len()), "text/plain; charset=utf-8"); err != nil {
		return err
	}

	// The database is updated only after the object is written: the reverse order would
	// announce a sealed log nobody could read.
	if err := c.store.SealTaskLog(ctx, target.RunName, target.TaskName, object); err != nil {
		return err
	}

	// The purge comes last, and its failure does not invalidate the sealing: orphaned
	// chunks cost storage, a missing log costs an investigation.
	for _, key := range ordered {
		if err := c.storage.Remove(ctx, key); err != nil {
			c.logger.Warn("purging a chunk", "object", key, "error", err)
		}
	}

	c.logger.Info("log sealed",
		"run", target.RunName, "task", target.TaskName, "object", object,
		"bytes", assembled.Len(), "chunks", len(ordered))
	return nil
}
