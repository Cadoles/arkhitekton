package logs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/blob"
)

// DefaultTail is how much is read by default when no offset is asked for.
//
// An integration log weighs tens of megabytes: loading it whole to display the end
// would be ruinous, and the end is what one looks at first — the failure is at the
// end.
const DefaultTail int64 = 64 << 10

// MaxRead bounds a single read. Beyond it, the caller pages by offset.
const MaxRead int64 = 4 << 20

// Segment is a portion of a log.
type Segment struct {
	Content []byte
	// Offset is the position of the first byte returned, within the complete log.
	Offset int64
	// NextOffset is the position to ask for to continue. Equal to the total size when
	// everything has been read.
	NextOffset int64
	// Size is the total size known at that instant. It grows while the task runs.
	Size int64
	// Sealed says the log is final: the size will not move again, and there is nothing
	// left to wait for.
	Sealed bool
}

// Reader reads a log, sealed or in flight.
//
// Both cases are served by the same interface because the caller should not have to
// know: a log in flight is a series of chunks, a sealed log is a single object, and
// a byte's position within the complete log is the same either way. That is what
// lets a client follow a run live and then keep reading after it is sealed, without
// a break.
type Reader struct {
	storage *blob.Client
}

func NewReader(storage *blob.Client) *Reader {
	return &Reader{storage: storage}
}

// Tail returns the end of the log.
func (r *Reader) Tail(ctx context.Context, runName, taskName string, length int64) (Segment, error) {
	if length <= 0 || length > MaxRead {
		length = DefaultTail
	}

	pieces, total, sealed, err := r.layout(ctx, runName, taskName)
	if err != nil {
		return Segment{}, err
	}

	offset := total - length
	if offset < 0 {
		offset = 0
	}
	return r.read(ctx, pieces, total, sealed, offset, length)
}

// From returns the log from an offset.
//
// This is the live-following operation: the client asks again from `NextOffset` and
// receives what was added. An offset beyond the end returns an empty segment rather
// than an error — the task simply produced nothing since.
func (r *Reader) From(ctx context.Context, runName, taskName string, offset, length int64) (Segment, error) {
	if offset < 0 {
		offset = 0
	}
	if length <= 0 || length > MaxRead {
		length = MaxRead
	}

	pieces, total, sealed, err := r.layout(ctx, runName, taskName)
	if err != nil {
		return Segment{}, err
	}
	return r.read(ctx, pieces, total, sealed, offset, length)
}

// LinesBefore counts the complete lines preceding an offset.
//
// It serves absolute numbering. The viewer only opens the end of a log, and without
// this count it numbered from what it had loaded: the same "#L42" then named two
// different lines depending on how much each side of a shared link had read.
//
// The cost is accepted: counting means walking everything before, hence downloading
// the beginning of the log that ranged reading exists to avoid. That is why the call
// is made only when a window opens, and never while following — the stream only
// appends, which renumbers nothing.
func (r *Reader) LinesBefore(ctx context.Context, runName, taskName string, offset int64) (int64, error) {
	if offset <= 0 {
		return 0, nil
	}

	pieces, _, _, err := r.layout(ctx, runName, taskName)
	if err != nil {
		return 0, err
	}

	var count int64
	for _, p := range pieces {
		if p.start >= offset {
			break
		}
		lines, err := r.countNewlines(ctx, p.key, min(offset-p.start, p.size))
		if err != nil {
			return 0, err
		}
		count += lines
	}
	return count, nil
}

// countNewlines counts the newlines in the first `length` bytes of an object.
//
// Read through a buffer rather than all at once: the beginning of an integration log
// weighs megabytes, and loading it entirely into memory just to count bytes would be
// gratuitously expensive.
func (r *Reader) countNewlines(ctx context.Context, key string, length int64) (int64, error) {
	if length <= 0 {
		return 0, nil
	}

	reader, err := r.storage.GetRange(ctx, key, 0, length)
	if err != nil {
		return 0, fmt.Errorf("reading %q: %w", key, err)
	}
	defer reader.Close()

	var count int64
	buffer := make([]byte, 32<<10)
	for {
		n, err := reader.Read(buffer)
		count += int64(bytes.Count(buffer[:n], newline))
		if errors.Is(err, io.EOF) {
			return count, nil
		}
		if err != nil {
			return 0, fmt.Errorf("reading %q: %w", key, err)
		}
	}
}

var newline = []byte{'\n'}

// piece is an object and its position within the complete log.
type piece struct {
	key   string
	start int64
	size  int64
}

// layout builds the map of the log.
//
// The sealed object wins over the chunks: after sealing the chunks are purged, but
// the purge may fail — it does not call the sealing into question. Reading both
// would produce a duplicated log.
func (r *Reader) layout(ctx context.Context, runName, taskName string) ([]piece, int64, bool, error) {
	object, err := r.storage.Keys.TaskLogObject(runName, taskName)
	if err != nil {
		return nil, 0, false, err
	}

	if info, err := r.storage.Stat(ctx, object); err == nil {
		return []piece{{key: object, start: 0, size: info.Size}}, info.Size, true, nil
	}

	prefix, err := r.storage.Keys.TaskLogPartsPrefix(runName, taskName)
	if err != nil {
		return nil, 0, false, err
	}

	chunks, err := r.storage.List(ctx, prefix)
	if err != nil {
		return nil, 0, false, err
	}
	if len(chunks) == 0 {
		return nil, 0, false, nil
	}

	// Keys carry the step's rank then the chunk's, zero-padded: lexicographic order is
	// the order of the log.
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].Key < chunks[j].Key })

	pieces := make([]piece, 0, len(chunks))
	var total int64
	for _, chunk := range chunks {
		pieces = append(pieces, piece{key: chunk.Key, start: total, size: chunk.Size})
		total += chunk.Size
	}
	return pieces, total, false, nil
}

func (r *Reader) read(
	ctx context.Context, pieces []piece, total int64, sealed bool, offset, length int64,
) (Segment, error) {
	segment := Segment{Offset: offset, NextOffset: offset, Size: total, Sealed: sealed}

	if offset >= total {
		// Nothing new. A normal case while following: the client asks again faster than the
		// task writes.
		segment.Offset = total
		segment.NextOffset = total
		return segment, nil
	}

	end := offset + length
	if end > total {
		end = total
	}

	content := make([]byte, 0, end-offset)
	for _, p := range pieces {
		pieceEnd := p.start + p.size
		if pieceEnd <= offset || p.start >= end {
			// A chunk entirely outside the window: neither read nor downloaded. That is the
			// point of the map — displaying the end of a log must not cost reading its
			// beginning.
			continue
		}

		from := max(offset-p.start, 0)
		to := min(end-p.start, p.size)

		chunk, err := r.readRange(ctx, p.key, from, to-from)
		if err != nil {
			return Segment{}, err
		}
		content = append(content, chunk...)
	}

	segment.Content = content
	segment.NextOffset = offset + int64(len(content))
	return segment, nil
}

func (r *Reader) readRange(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	if length <= 0 {
		return nil, nil
	}

	reader, err := r.storage.GetRange(ctx, key, offset, length)
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", key, err)
	}
	defer reader.Close()

	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", key, err)
	}
	return content, nil
}
