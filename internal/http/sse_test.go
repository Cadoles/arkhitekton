package http

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A multi-line payload must be split into as many `data:` lines: a raw newline would
// terminate the event, and the client would receive only the log's first line.
func TestMultilinePayloadIsSplitAcrossDataFields(t *testing.T) {
	recorder := httptest.NewRecorder()
	stream, ok := newSSEWriter(recorder)
	if !ok {
		t.Fatal("the ResponseRecorder should support streaming")
	}

	if err := stream.event("log", "42", "first line\nsecond line"); err != nil {
		t.Fatalf("emission: %v", err)
	}

	body := recorder.Body.String()
	if count := strings.Count(body, "data: "); count != 2 {
		t.Errorf("%d data field(s), expected 2:\n%s", count, body)
	}
	if !strings.Contains(body, "id: 42\n") {
		t.Errorf("resume identifier missing:\n%s", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Errorf("the event is not terminated by an empty line:\n%q", body)
	}
}

// nginx buffers an SSE stream by default: the interface would show a frozen run then jump to
// its final state.
func TestStreamHeadersDisableBuffering(t *testing.T) {
	recorder := httptest.NewRecorder()
	if _, ok := newSSEWriter(recorder); !ok {
		t.Fatal("streaming not supported")
	}

	for header, expected := range map[string]string{
		"Content-Type":      "text/event-stream",
		"Cache-Control":     "no-cache",
		"X-Accel-Buffering": "no",
	} {
		if got := recorder.Header().Get(header); got != expected {
			t.Errorf("%s = %q, expected %q", header, got, expected)
		}
	}
}

// A comment must trigger no event on the client side: that is what allows keeping the
// connection open without polluting the interface.
func TestHeartbeatIsAComment(t *testing.T) {
	recorder := httptest.NewRecorder()
	stream, _ := newSSEWriter(recorder)

	if err := stream.comment("heartbeat"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	body := recorder.Body.String()
	if !strings.HasPrefix(body, ": ") {
		t.Errorf("the heartbeat is not a comment: %q", body)
	}
	if strings.Contains(body, "data:") || strings.Contains(body, "event:") {
		t.Errorf("the heartbeat carries an event: %q", body)
	}
}

// A stream outlives the server's write timeout. Run against a real server: a recorder has no
// deadline, and the cut only shows on a connection.
func TestStreamOutlivesTheServerWriteTimeout(t *testing.T) {
	const writeTimeout = 100 * time.Millisecond

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		stream, ok := newSSEWriter(w)
		if !ok {
			t.Error("streaming not supported")
			return
		}
		time.Sleep(3 * writeTimeout)
		if err := stream.comment("heartbeat"); err != nil {
			t.Errorf("heartbeat past the write timeout: %v", err)
		}
	}))
	server.Config.WriteTimeout = writeTimeout
	server.Start()
	defer server.Close()

	response, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatalf("opening the stream: %v", err)
	}
	defer response.Body.Close()

	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil {
		t.Fatalf("the stream was cut: %v", err)
	}
	if line != ": heartbeat\n" {
		t.Errorf("read %q", line)
	}
}
