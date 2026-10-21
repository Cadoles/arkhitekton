// Package guest is the plugin side of an interpreter compiled to WebAssembly.
//
// The dashboard runs such a module in a sandbox: an empty WASI (no directory, no environment, no
// argument, no network), bounded memory, time and output. It hands the module the run's state and
// its parsed records as JSON, and reads facts back. This package does both ends of that exchange
// for an interpreter written in Go:
//
//	package main
//
//	import "forge.cadoles.com/vcarroy/arkhitekton/sdk/guest"
//
//	func init() { guest.Register(reader{}) }
//
//	func main() {}
//
// built with `GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared`. The module runs as a reactor:
// its `main` is never called, which is why the interpreter is registered in an `init`.
//
// The exchange, version 1 of the ABI: three exports, `arkhitekton_abi() i32` returning 1,
// `arkhitekton_alloc(size i32) i32` reserving the input, and `arkhitekton_interpret(ptr, len i32)
// i64` returning the output's `ptr<<32 | len`. Input and output are the JSON of [Input] and
// [Output]; the sdk's types travel by their Go field names.
package guest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse/security"
)

// ABI is the version of the exchange.
const ABI = 1

// Input is what the dashboard hands the module.
type Input struct {
	ABI     int                 `json:"abi"`
	Run     capability.RunState `json:"run"`
	Records []Record            `json:"records"`
}

// Record is a parse.Record as it travels: the payload as JSON, the bytes of a `raw` artifact in
// base64, the parsing failure as text.
type Record struct {
	Artifact string          `json:"artifact"`
	Member   string          `json:"member,omitempty"`
	Object   string          `json:"object"`
	Parser   parse.ID        `json:"parser"`
	Tool     string          `json:"tool"`
	Payload  json.RawMessage `json:"payload,omitempty"`
	Bytes    string          `json:"bytes,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// Output is what the module hands back.
type Output struct {
	Sets  []facts.Set `json:"sets"`
	Error string      `json:"error,omitempty"`
}

// EncodeRecord prepares a record for the module. Used by the dashboard.
func EncodeRecord(record parse.Record) (Record, error) {
	wire := Record{
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
			return Record{}, fmt.Errorf("record %s of %s: %w", record.Parser, record.Tool, err)
		}
		wire.Payload = raw
	}
	return wire, nil
}

// DecodeRecord rebuilds a record in its sdk type, chosen by its parser.
func DecodeRecord(wire Record) (parse.Record, error) {
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

// Handle answers one input: it rebuilds the records, runs the interpreter and encodes its facts.
// An interpreter that panics answers with an error rather than bringing the module down.
func Handle(input []byte, interpreter capability.Interpreter) (output []byte) {
	answer := func(result Output) []byte {
		encoded, err := json.Marshal(result)
		if err != nil {
			encoded, _ = json.Marshal(Output{Error: "encoding the facts: " + err.Error()})
		}
		return encoded
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			output = answer(Output{Error: fmt.Sprintf("the interpreter panicked: %v", recovered)})
		}
	}()

	var decoded Input
	if err := json.Unmarshal(input, &decoded); err != nil {
		return answer(Output{Error: "reading the input: " + err.Error()})
	}
	if decoded.ABI != ABI {
		return answer(Output{Error: fmt.Sprintf("ABI %d, this module speaks %d", decoded.ABI, ABI)})
	}
	if interpreter == nil {
		return answer(Output{Error: "no interpreter registered: call guest.Register in an init"})
	}
	records := make([]parse.Record, 0, len(decoded.Records))
	for _, wire := range decoded.Records {
		record, err := DecodeRecord(wire)
		if err != nil {
			return answer(Output{Error: err.Error()})
		}
		records = append(records, record)
	}
	sets, err := interpreter.Interpret(context.Background(), decoded.Run, records)
	result := Output{Sets: sets}
	if err != nil {
		result.Error = err.Error()
	}
	return answer(result)
}

// registered is the module's interpreter, set by Register.
var registered capability.Interpreter

// Register declares the module's interpreter. Call it from an `init`: a reactor module's `main`
// never runs.
func Register(interpreter capability.Interpreter) { registered = interpreter }
