package http

import (
	"fmt"
	"net/http"
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

// sseWriter encapsulates the protocol.
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// newSSEWriter prepares the response. Returns false if the client cannot be served as a
// stream — the case of a buffering intermediary.
func newSSEWriter(w http.ResponseWriter) (*sseWriter, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	// Without that header, nginx accumulates the events and delivers them in a block: the
	// interface shows a frozen run, then jumps to its final state.
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	return &sseWriter{w: w, flusher: flusher}, true
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

	if _, err := s.w.Write([]byte(builder.String())); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// comment emits a heartbeat, invisible to the client.
func (s *sseWriter) comment(text string) error {
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}
