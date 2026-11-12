package http

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

/*
 * Server-sent event streams.
 *
 * Live following is not fed by an in-memory channel. The reason is structural: the collector runs
 * in the `worker`, the SSE stream is served by the `api`, and they are two processes. An
 * in-memory channel does not link them.
 *
 * Three ways were possible:
 *
 * - passing each line through PostgreSQL NOTIFY — a high volume for a channel whose payload is
 * bounded at 8,000 bytes;
 * - serving the stream from the worker — it has neither an HTTP server nor a Service, and
 * exposing it would make the single instance a point on the path of user traffic;
 * - **following the storage**, chosen here.
 *
 * The api periodically re-reads what the collector wrote and pushes the difference. The latency is
 * therefore bounded by the collector's flush interval, not by the polling. The method has three
 * advantages that amply make up for that delay: it works with several `api` replicas, it is
 * resumed to the byte by `Last-Event-ID`, and it is exactly the same read path as the history —
 * there are not two implementations to keep in agreement.
 */

// sseHeartbeat keeps the connection open through intermediaries.
//
// nginx closes an idle connection, and the dashboard's ingress carries a one-hour read
// timeout: a pipeline silent for that long would see its stream cut for no visible reason. An
// SSE comment triggers no event on the client side.
const sseHeartbeat = 20 * time.Second

// sseWriteTimeout bounds one write to a stream.
//
// The server's write timeout counts from the request and would cut every stream once it has run
// out. Renewed at each write, this deadline lets a stream last while a stalled client still goes.
const sseWriteTimeout = 30 * time.Second

// sseWriter encapsulates the protocol.
type sseWriter struct {
	w       http.ResponseWriter
	control *http.ResponseController
}

// newSSEWriter prepares the response. Returns false if the client cannot be served as a
// stream — the case of a buffering intermediary.
func newSSEWriter(w http.ResponseWriter) (*sseWriter, bool) {
	if _, ok := w.(http.Flusher); !ok {
		return nil, false
	}

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	// Without that header, nginx accumulates the events and delivers them in a block: the
	// interface shows a frozen run, then jumps to its final state.
	header.Set("X-Accel-Buffering", "no")
	stream := &sseWriter{w: w, control: http.NewResponseController(w)}
	w.WriteHeader(http.StatusOK)
	// A failed flush shows at the next write: the status is already sent.
	_ = stream.send("")
	return stream, true
}

// event emits a named event, with its resume identifier.
func (s *sseWriter) event(name, id, data string) error {
	var builder strings.Builder
	if id != "" {
		fmt.Fprintf(&builder, "id: %s\n", id)
	}
	if name != "" {
		fmt.Fprintf(&builder, "event: %s\n", name)
	}
	// A multi-line payload must be split: a raw newline would terminate the event, and the client
	// would receive only its first line.
	for line := range strings.SplitSeq(data, "\n") {
		fmt.Fprintf(&builder, "data: %s\n", line)
	}
	builder.WriteString("\n")

	return s.send(builder.String())
}

// comment emits a heartbeat, invisible to the client.
func (s *sseWriter) comment(text string) error {
	return s.send(fmt.Sprintf(": %s\n\n", text))
}

// send writes and flushes under a fresh deadline. A client that left is the ordinary end of a
// stream; one that stays connected without reading is worth a line in the logs.
func (s *sseWriter) send(payload string) error {
	err := s.control.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
	if err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if _, err := io.WriteString(s.w, payload); err != nil {
		return streamFailure(err)
	}
	return streamFailure(s.control.Flush())
}

func streamFailure(err error) error {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		slog.Default().Warn("an event stream stalled: its client stopped reading", "error", err)
	}
	return err
}
