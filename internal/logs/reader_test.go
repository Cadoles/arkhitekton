package logs

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// putChunk writes a chunk at a given step and sequence rank.
func putChunk(t *testing.T, target Target, index, sequence int, container, content string) {
	t.Helper()

	storage := testStorage(t)
	key, err := storage.Keys.TaskLogChunk(target.RunName, target.TaskName, container, index, sequence)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	if err := storage.Put(context.Background(), key,
		strings.NewReader(content), int64(len(content)), "text/plain"); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// A log in flight and a sealed log read the same way: that is what lets one follow a
// run and then keep reading after it is sealed, without the client changing mode.
func TestUnsealedLogIsReadAsOneContinuousStream(t *testing.T) {
	storage := testStorage(t)
	ctx := context.Background()

	target := uniqueTarget(t, Step{Name: "tests", Container: "step-run-tests"})
	cleanup(t, storage, target)

	putChunk(t, target, 0, 1, "step-run-tests", "AAAA")
	putChunk(t, target, 0, 2, "step-run-tests", "BBBB")
	// The next step: its rank places it after, despite an alphabetically earlier name.
	putChunk(t, target, 1, 1, "step-publish-results", "CCCC")

	reader := NewReader(storage)

	segment, err := reader.From(ctx, target.RunName, target.TaskName, 0, 0)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if string(segment.Content) != "AAAABBBBCCCC" {
		t.Errorf("contenu = %q", segment.Content)
	}
	if segment.Sealed {
		t.Error("a chunked log must not be announced as sealed")
	}
	if segment.Size != 12 || segment.NextOffset != 12 {
		t.Errorf("size = %d, next offset = %d", segment.Size, segment.NextOffset)
	}
}

// A ranged read must touch only the chunks concerned: displaying the end of a
// fifty-megabyte log must not cost reading its beginning.
func TestTailReadsOnlyTheEnd(t *testing.T) {
	storage := testStorage(t)
	ctx := context.Background()

	target := uniqueTarget(t, Step{Name: "tests", Container: "step-run-tests"})
	cleanup(t, storage, target)

	putChunk(t, target, 0, 1, "step-run-tests", strings.Repeat("x", 1000))
	putChunk(t, target, 0, 2, "step-run-tests", "END-OF-LOG")

	reader := NewReader(storage)
	segment, err := reader.Tail(ctx, target.RunName, target.TaskName, 14)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}

	if string(segment.Content) != "END-OF-LOG" {
		t.Errorf("contenu = %q", segment.Content)
	}
	if segment.Offset != 1000 {
		t.Errorf("offset = %d, expected 1000", segment.Offset)
	}
}

// Following live: the client asks again from the returned offset and receives only
// what was added.
func TestFromOffsetReturnsOnlyWhatWasAppended(t *testing.T) {
	storage := testStorage(t)
	ctx := context.Background()

	target := uniqueTarget(t, Step{Name: "tests", Container: "step-run-tests"})
	cleanup(t, storage, target)

	putChunk(t, target, 0, 1, "step-run-tests", "premier\n")
	reader := NewReader(storage)

	first, err := reader.From(ctx, target.RunName, target.TaskName, 0, 0)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}

	// Nothing new: an empty segment, not an error.
	empty, err := reader.From(ctx, target.RunName, target.TaskName, first.NextOffset, 0)
	if err != nil {
		t.Fatalf("empty read: %v", err)
	}
	if len(empty.Content) != 0 {
		t.Errorf("contenu inattendu: %q", empty.Content)
	}

	putChunk(t, target, 0, 2, "step-run-tests", "second\n")

	next, err := reader.From(ctx, target.RunName, target.TaskName, first.NextOffset, 0)
	if err != nil {
		t.Fatalf("seconde lecture: %v", err)
	}
	if string(next.Content) != "second\n" {
		t.Errorf("content = %q, expected only what was added", next.Content)
	}
}

// After sealing, the single object wins over the chunks. The purge may fail without
// calling the sealing into question: reading both would produce duplicates.
func TestSealedObjectWinsOverLeftoverChunks(t *testing.T) {
	storage := testStorage(t)
	ctx := context.Background()

	target := uniqueTarget(t, Step{Name: "tests", Container: "step-run-tests"})
	cleanup(t, storage, target)

	putChunk(t, target, 0, 1, "step-run-tests", "forgotten chunk\n")

	object, err := storage.Keys.TaskLogObject(target.RunName, target.TaskName)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	const sealed = "sealed log\n"
	if err := storage.Put(ctx, object, strings.NewReader(sealed), int64(len(sealed)), "text/plain"); err != nil {
		t.Fatalf("writing the sealed object: %v", err)
	}

	segment, err := NewReader(storage).From(ctx, target.RunName, target.TaskName, 0, 0)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if string(segment.Content) != sealed {
		t.Errorf("content = %q, expected the sealed object alone", segment.Content)
	}
	if !segment.Sealed {
		t.Error("the log should be announced as sealed")
	}
}

// A task with no log — never started, or already purged — returns an empty segment,
// not an error: it is a normal state of the interface.
func TestMissingLogIsEmptyNotAnError(t *testing.T) {
	storage := testStorage(t)
	target := uniqueTarget(t, Step{Name: "tests", Container: "step-run-tests"})

	segment, err := NewReader(storage).Tail(context.Background(), target.RunName, target.TaskName, 0)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if len(segment.Content) != 0 || segment.Size != 0 || segment.Sealed {
		t.Errorf("segment = %+v", segment)
	}
}

func TestReadIsBounded(t *testing.T) {
	storage := testStorage(t)
	ctx := context.Background()

	target := uniqueTarget(t, Step{Name: "tests", Container: "step-run-tests"})
	cleanup(t, storage, target)

	putChunk(t, target, 0, 1, "step-run-tests", strings.Repeat("y", 2000))

	// An outsized length is clamped to the ceiling rather than honoured: a client asking
	// for all of a fifty-megabyte log must not be able to exhaust the server's memory.
	segment, err := NewReader(storage).From(ctx, target.RunName, target.TaskName, 0, MaxRead*10)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if int64(len(segment.Content)) != 2000 {
		t.Errorf("%d octets lus", len(segment.Content))
	}
	if fmt.Sprint(segment.Size) != "2000" {
		t.Errorf("taille = %d", segment.Size)
	}
}

// Absolute numbering rests on this: how many complete lines precede the displayed
// window.
//
// Without the count, the viewer numbered from what it had loaded, and "line 42"
// named two different lines on either side of a shared link — one having read only
// the end, the other the whole log.
func TestLinesBeforeCountsAcrossChunks(t *testing.T) {
	storage := testStorage(t)
	ctx := context.Background()

	target := uniqueTarget(t, Step{Name: "tests", Container: "step-run-tests"})
	cleanup(t, storage, target)

	// Two chunks, with the cut falling mid-line: the real case, since the collector
	// splits on size and not on newlines.
	putChunk(t, target, 0, 1, "step-run-tests", "one\ntwo\nthr")
	putChunk(t, target, 0, 2, "step-run-tests", "is\nquatre\n")

	reader := NewReader(storage)

	// A zero offset costs nothing and precedes nothing.
	if count, err := reader.LinesBefore(ctx, target.RunName, target.TaskName, 0); err != nil || count != 0 {
		t.Errorf("before the start: %d, %v", count, err)
	}

	// Position 9: just after the first two lines, hence two complete lines.
	if count, err := reader.LinesBefore(ctx, target.RunName, target.TaskName, 9); err != nil || count != 2 {
		t.Errorf("after two lines: %d, %v — expected 2", count, err)
	}

	// Position 14, mid-word and therefore in the second chunk: the same two complete
	// lines precede, the third is started. That is what makes a window opened
	// mid-line number it as the line it belongs to.
	if count, err := reader.LinesBefore(ctx, target.RunName, target.TaskName, 14); err != nil || count != 2 {
		t.Errorf("mid-line: %d, %v — expected 2", count, err)
	}

	// The end: every line precedes.
	if count, err := reader.LinesBefore(ctx, target.RunName, target.TaskName, 22); err != nil || count != 4 {
		t.Errorf("at the end: %d, %v — expected 4", count, err)
	}
}
