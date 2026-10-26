package native

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"

	"github.com/hashicorp/go-hclog"
)

// newHCLogger gives go-plugin the dashboard's log, at info: its debug lines narrate the handshake
// and every connection.
func newHCLogger(logger *slog.Logger) hclog.Logger {
	return hclog.New(&hclog.LoggerOptions{
		Name: "go-plugin", Level: hclog.Info, Output: &lineWriter{logger: logger}, JSONFormat: true,
	})
}

// lineWriter logs what a plugin writes, line by line. A line in slog's or hclog's JSON keeps its
// level, message and fields; any other line is logged as it is.
type lineWriter struct {
	logger *slog.Logger

	mu      sync.Mutex
	pending []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, p...)
	for {
		end := bytes.IndexByte(w.pending, '\n')
		if end < 0 {
			break
		}
		w.log(string(w.pending[:end]))
		w.pending = w.pending[end+1:]
	}
	return len(p), nil
}

func (w *lineWriter) log(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	var fields map[string]any
	if json.Unmarshal([]byte(line), &fields) != nil {
		w.logger.Info("plugin output", "line", line)
		return
	}
	message, level := "", slog.LevelInfo
	for _, key := range []string{"msg", "@message"} {
		if text, ok := fields[key].(string); ok {
			message = text
			delete(fields, key)
		}
	}
	for _, key := range []string{"level", "@level"} {
		if text, ok := fields[key].(string); ok {
			_ = level.UnmarshalText([]byte(strings.ToUpper(text)))
			delete(fields, key)
		}
	}
	for _, key := range []string{"time", "@timestamp"} {
		delete(fields, key)
	}
	attributes := make([]any, 0, 2*len(fields))
	for key, value := range fields {
		attributes = append(attributes, strings.TrimPrefix(key, "@"), value)
	}
	w.logger.Log(context.Background(), level, message, attributes...)
}
