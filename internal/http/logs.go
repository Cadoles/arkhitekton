package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/logs"
)

// LogReader is what the handlers need from the log reader.
type LogReader interface {
	Tail(ctx context.Context, runName, taskName string, length int64) (logs.Segment, error)
	From(ctx context.Context, runName, taskName string, offset, length int64) (logs.Segment, error)
	// LinesBefore numbers the returned window within the whole log.
	LinesBefore(ctx context.Context, runName, taskName string, offset int64) (int64, error)
}

// logPollInterval separates two reads of the store while following.
//
// Shorter than the collector's flush interval would be waste: nothing new can appear
// between two flushes.
const logPollInterval = 2 * time.Second

// LogsHandler serves the logs, both as a single read and as a follow.
type LogsHandler struct {
	reader LogReader
}

func NewLogsHandler(reader LogReader) *LogsHandler {
	return &LogsHandler{reader: reader}
}

type logPayload struct {
	Content    string `json:"content"`
	Offset     int64  `json:"offset"`
	NextOffset int64  `json:"next_offset"`
	Size       int64  `json:"size"`
	// FirstLine is the rank, within the whole log, of the first line returned. Without it the
	// client numbers from what it has loaded, and "line 42" does not name the same thing
	// depending on how much was read — a shared link then lands beside the point.
	FirstLine int64 `json:"first_line"`
	// Sealed tells the interface there is no point in following any more: the log will not
	// change again.
	Sealed bool `json:"sealed"`
}

// Read returns a portion of a log.
func (h *LogsHandler) Read(w http.ResponseWriter, r *http.Request) {
	runName := r.PathValue("run")
	taskName := r.PathValue("task")

	length := intQuery(r, "length", 0)

	var segment logs.Segment
	var err error
	if raw := r.URL.Query().Get("offset"); raw != "" {
		segment, err = h.reader.From(r.Context(), runName, taskName, intQuery(r, "offset", 0), length)
	} else {
		segment, err = h.reader.Tail(r.Context(), runName, taskName, length)
	}
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the log", "")
		return
	}

	// The counting happens only for reading a window, never for following: the stream only
	// appends, which renumbers nothing.
	firstLine, err := h.reader.LinesBefore(r.Context(), runName, taskName, segment.Offset)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the log", "")
		return
	}

	writeJSON(w, http.StatusOK, logPayload{
		Content:    string(segment.Content),
		Offset:     segment.Offset,
		NextOffset: segment.NextOffset,
		Size:       segment.Size,
		FirstLine:  firstLine,
		Sealed:     segment.Sealed,
	})
}

// Stream follows a log live.
//
// Resuming goes through `Last-Event-ID`, whose value is the byte offset: a client that
// reconnects resumes exactly where it stopped, with no duplicate and no gap. It is the
// standard SSE mechanism, and it fits here because the log is a sequence of bytes numbered
// stably.
func (h *LogsHandler) Stream(w http.ResponseWriter, r *http.Request) {
	runName := r.PathValue("run")
	taskName := r.PathValue("task")

	stream, ok := newSSEWriter(w)
	if !ok {
		writeProblem(w, http.StatusInternalServerError, "Streaming not supported", "")
		return
	}

	// With no resume point, the client first receives the end of the log: connecting to a
	// running run and seeing nothing until the next line would be disconcerting.
	offset := int64(-1)

	// The query parameter serves the first connection: EventSource does not allow setting a
	// header, and a client that has already loaded the end through the read API must be able
	// to start the stream after it rather than receive that end a second time.
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	// Last-Event-ID wins: it comes from the browser's reconnections, and its value is more
	// recent than the one written into the URL at the first subscription.
	if resume := r.Header.Get("Last-Event-ID"); resume != "" {
		if parsed, err := strconv.ParseInt(resume, 10, 64); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	ticker := time.NewTicker(logPollInterval)
	defer ticker.Stop()

	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()

	for {
		segment, err := h.next(r.Context(), runName, taskName, offset)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			_ = stream.event("error", "", "cannot read the log")
			return
		}
		offset = segment.NextOffset

		if len(segment.Content) > 0 {
			if err := stream.event("log", strconv.FormatInt(segment.NextOffset, 10), string(segment.Content)); err != nil {
				return
			}
		}

		if segment.Sealed && offset >= segment.Size {
			// The log will not change again: announcing it lets the client close the connection
			// instead of polling indefinitely.
			_ = stream.event("sealed", strconv.FormatInt(segment.Size, 10), "")
			return
		}

		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if err := stream.comment("battement"); err != nil {
				return
			}
		case <-ticker.C:
		}
	}
}

// Raw serves the whole log as text.
//
// Two uses for one route, told apart by `download`: opening the raw log in a tab — the
// viewer virtualises, which prevents searching with the browser or selecting everything —
// and saving it as a file. The resource is the same, only the presentation changes; two
// routes for that would have duplicated the read.
//
// The content is served as stored, kubelet timestamps included. That is what "raw" means:
// what the viewer strips for display must be found here, otherwise the downloaded file
// could no longer be cross-referenced with another log.
func (h *LogsHandler) Raw(w http.ResponseWriter, r *http.Request) {
	runName := r.PathValue("run")
	taskName := r.PathValue("task")

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.URL.Query().Get("download") != "" {
		// The file name is composed from both identifiers: a downloads folder where ten files are
		// called `log.txt` is useless.
		w.Header().Set("Content-Disposition",
			fmt.Sprintf("attachment; filename=%q", sanitizeFilename(runName+"-"+taskName)+".txt"))
	}

	// The read is paginated because the reader bounds every call to MaxRead: an integration log
	// far exceeds that size. The chunks are written as they come rather than accumulated — the
	// whole log has no reason to sit in the server's memory before leaving.
	var offset int64
	for {
		segment, err := h.reader.From(r.Context(), runName, taskName, offset, 0)
		if err != nil {
			if offset == 0 {
				writeProblem(w, http.StatusInternalServerError, "Cannot read the log", "")
				return
			}
			// The header has gone and part of the body with it: there is no status code left to change.
			// Interrupting leaves a truncated response, which is the least bad of the possible states.
			return
		}

		if _, err := w.Write(segment.Content); err != nil {
			// Tab closed mid-download: nothing to report.
			return
		}

		// Progress is the stopping condition, and not merely equality with the size: a log growing
		// during the read would make this loop run indefinitely if it waited to catch up.
		if segment.NextOffset <= offset || segment.NextOffset >= segment.Size {
			return
		}
		offset = segment.NextOffset
	}
}

// sanitizeFilename keeps only what crosses a header and a file system unharmed.
// PipelineRun names are already DNS identifiers, but the header is built here and nothing
// guarantees it will stay that way.
func sanitizeFilename(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, name)
}

func (h *LogsHandler) next(
	ctx context.Context, runName, taskName string, offset int64,
) (logs.Segment, error) {
	if offset < 0 {
		return h.reader.Tail(ctx, runName, taskName, 0)
	}
	return h.reader.From(ctx, runName, taskName, offset, 0)
}

func intQuery(r *http.Request, name string, fallback int64) int64 {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		// An unreadable display parameter falls back to the default: refusing the request would
		// teach nobody anything.
		return fallback
	}
	return value
}
