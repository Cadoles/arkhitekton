// Package records carries a run's parsed records to a native plugin: the payload as JSON, the
// bytes of a `raw` artifact in base64, the parsing failure as text. Both ends use it, so that a
// payload type added to the sdk is added once.
package records

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse/security"
)

// Wire is a parse.Record as it travels: the payload as JSON, the bytes of a `raw` artifact in
// base64, the parsing failure as text.
type Wire struct {
	Artifact string          `json:"artifact"`
	Member   string          `json:"member,omitempty"`
	Object   string          `json:"object"`
	Parser   parse.ID        `json:"parser"`
	Tool     string          `json:"tool"`
	Payload  json.RawMessage `json:"payload,omitempty"`
	Bytes    string          `json:"bytes,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// Encode prepares a record for the plugin. Used by the dashboard.
func Encode(record parse.Record) (Wire, error) {
	wire := Wire{
		Artifact: record.Artifact, Member: record.Member, Object: record.Object,
		Parser: record.Parser, Tool: record.Tool,
	}
	if record.Err != nil {
		wire.Error = record.Err.Error()
	}
	switch payload := record.Payload.(type) {
	case nil:
	case parse.Bytes:
		wire.Bytes = base64.StdEncoding.EncodeToString(payload)
	default:
		raw, err := json.Marshal(payload)
		if err != nil {
			return Wire{}, fmt.Errorf("record %s of %s: %w", record.Parser, record.Tool, err)
		}
		wire.Payload = raw
	}
	return wire, nil
}

// Decode rebuilds a record in its sdk type, chosen by its parser. Used by the plugin.
func Decode(wire Wire) (parse.Record, error) {
	record := parse.Record{
		Artifact: wire.Artifact, Member: wire.Member, Object: wire.Object,
		Parser: wire.Parser, Tool: wire.Tool,
	}
	if wire.Error != "" {
		record.Err = errors.New(wire.Error)
	}
	if wire.Parser == parse.Raw {
		raw, err := base64.StdEncoding.DecodeString(wire.Bytes)
		if err != nil {
			return parse.Record{}, fmt.Errorf("record %s of %s: %w", wire.Parser, wire.Tool, err)
		}
		record.Payload = parse.Bytes(raw)
		return record, nil
	}
	if len(wire.Payload) == 0 {
		return record, nil
	}
	payload, err := decodePayload(wire.Parser, wire.Payload)
	if err != nil {
		return parse.Record{}, fmt.Errorf("record %s of %s: %w", wire.Parser, wire.Tool, err)
	}
	record.Payload = payload
	return record, nil
}

func decodePayload(parser parse.ID, raw []byte) (any, error) {
	switch parser {
	case parse.JUnitXML:
		return decode[parse.JUnit](raw)
	case parse.MochawesomeJSON:
		return decode[parse.Mochawesome](raw)
	case parse.PHPStanJSON:
		return decode[parse.PHPStan](raw)
	case parse.CSFixerJSON:
		return decode[parse.CSFixer](raw)
	case parse.SecuritySummaryJSON:
		return decode[security.Summary](raw)
	case parse.SecurityFindingsJSONL:
		return decode[[]security.Finding](raw)
	case parse.Attach:
		return decode[parse.Attached](raw)
	case parse.K6SummaryJSON:
		// A metric's error does not travel: an error value has no JSON. Its statistics do.
		var wire struct {
			Metrics map[string]struct {
				Values     map[string]float64
				Thresholds map[string]bool
			}
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			return nil, err
		}
		report := parse.K6{Metrics: make(map[string]parse.K6Metric, len(wire.Metrics))}
		for name, metric := range wire.Metrics {
			report.Metrics[name] = parse.K6Metric{Values: metric.Values, Thresholds: metric.Thresholds}
		}
		return report, nil
	}
	return nil, fmt.Errorf("no payload type for parser %q", parser)
}

func decode[T any](raw []byte) (any, error) {
	var value T
	err := json.Unmarshal(raw, &value)
	return value, err
}
