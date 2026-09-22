package logs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/blob"
)

// chunkWriter accumulates a step's lines and writes them in chunks.
//
// Each chunk is a complete object, durable as soon as it is written. That is what
// makes capture resistant to the worker stopping: what is written is acquired, and
// resuming starts again from the last known line.
type chunkWriter struct {
	storage *blob.Client
	target  Target
	step    Step
	// index is the step's rank within the task. It goes into the key so that the
	// lexicographic order of the chunks is the order of the log.
	index int

	limit    int
	buffer   bytes.Buffer
	sequence int

	// lastTimestamp bounds the resume. At start-up it is the timestamp of the last line
	// already stored; afterwards, that of the last accepted line.
	lastTimestamp *time.Time
}

// newChunkWriter prepares the writing and determines the resume point.
func newChunkWriter(
	ctx context.Context, storage *blob.Client, target Target, step Step, index, limit int,
) (*chunkWriter, error) {
	writer := &chunkWriter{storage: storage, target: target, step: step, index: index, limit: limit}

	prefix, err := storage.Keys.TaskLogChunkPrefix(target.RunName, target.TaskName, step.Container, index)
	if err != nil {
		return nil, err
	}

	existing, err := storage.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	if len(existing) == 0 {
		return writer, nil
	}

	keys := make([]string, 0, len(existing))
	for _, object := range existing {
		keys = append(keys, object.Key)
	}
	sortStrings(keys)

	writer.sequence = len(keys)

	// The resume point is read from the last chunk, not from the database: the store is
	// the source of truth about what was written, and a database ahead of it would lose
	// lines.
	last := keys[len(keys)-1]
	timestamp, err := lastTimestampOf(ctx, storage, last)
	if err != nil {
		return nil, fmt.Errorf("reading the resume point in %q: %w", last, err)
	}
	writer.lastTimestamp = timestamp

	return writer, nil
}

// accept discards lines already stored.
//
// The Kubernetes API's `sinceTime` has second precision: it necessarily returns
// lines already known after a resume. Duplicating them would be visible in the log;
// discarding them here is possible because kubelet timestamps every line to the
// nanosecond.
//
// A line with no usable timestamp is always accepted: losing it over a formatting
// flaw would be the wrong trade.
func (w *chunkWriter) accept(line Line) bool {
	if w.lastTimestamp == nil || line.Timestamp.IsZero() {
		return true
	}
	return line.Timestamp.After(*w.lastTimestamp)
}

// append adds a line to the buffer, and writes a chunk when the threshold is met.
func (w *chunkWriter) append(ctx context.Context, raw string) error {
	if !strings.HasSuffix(raw, "\n") {
		raw += "\n"
	}
	w.buffer.WriteString(raw)

	if line := parseLine(raw); !line.Timestamp.IsZero() {
		timestamp := line.Timestamp
		w.lastTimestamp = &timestamp
	}

	if w.buffer.Len() >= w.limit {
		return w.flush(ctx)
	}
	return nil
}

// flush writes the buffer as a new chunk. With no content, it does nothing.
func (w *chunkWriter) flush(ctx context.Context) error {
	if w.buffer.Len() == 0 {
		return nil
	}

	w.sequence++
	key, err := w.storage.Keys.TaskLogChunk(
		w.target.RunName, w.target.TaskName, w.step.Container, w.index, w.sequence)
	if err != nil {
		return err
	}

	payload := w.buffer.Bytes()
	if err := w.storage.Put(ctx, key,
		bytes.NewReader(payload), int64(len(payload)), "text/plain; charset=utf-8"); err != nil {
		// The buffer is kept: the next attempt will rewrite everything. Emptying it here
		// would permanently lose lines already read from the pod, which nobody can read
		// again.
		return err
	}

	w.buffer.Reset()
	return nil
}

// readAll reads a stream in full. A named wrapper to keep the io import out of the
// rest of the file.
func readAll(reader io.Reader) ([]byte, error) { return io.ReadAll(reader) }

// lastTimestampOf returns the timestamp of an object's last line.
func lastTimestampOf(ctx context.Context, storage *blob.Client, key string) (*time.Time, error) {
	reader, err := storage.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	// The chunk is read back in full. A ranged read from the end would do, but a chunk
	// weighs at most the flush threshold — one megabyte — and this happens once per step
	// at start-up.
	content, err := readAll(reader)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := parseLine(lines[i]); !line.Timestamp.IsZero() {
			timestamp := line.Timestamp
			return &timestamp, nil
		}
	}
	// A chunk with no timestamped line: the resume will be unbounded, at the risk of
	// duplicates. Preferable to a skip that would lose lines.
	return nil, nil
}

func sortStrings(values []string) { sort.Strings(values) }
