package native

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/rpc/pluginv1"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/records"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// readTimeout bounds the reading of one run by a plugin's program.
const readTimeout = 2 * time.Minute

// Interpreter reads a pipeline's runs by asking its plugin's program.
type Interpreter struct {
	// Process finds the program when a run is read: it may be launched, replaced or stopped
	// between two runs.
	Process  func() (*Process, error)
	Pipeline string
}

// Interpret hands the program the run's state and records, and reads its facts back.
func (i Interpreter) Interpret(ctx context.Context, run capability.RunState, read []parse.Record) ([]facts.Set, error) {
	process, err := i.Process()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	conn, err := process.Connection(ctx)
	if err != nil {
		return nil, err
	}
	state, err := json.Marshal(run)
	if err != nil {
		return nil, err
	}
	wires := make([]records.Wire, 0, len(read))
	for _, record := range read {
		wire, err := records.Encode(record)
		if err != nil {
			return nil, err
		}
		wires = append(wires, wire)
	}
	encoded, err := json.Marshal(wires)
	if err != nil {
		return nil, err
	}
	answer, err := conn.Plugin.Interpret(ctx, &pluginv1.InterpretRequest{
		Pipeline: i.Pipeline, RunState: state, Records: encoded,
	})
	if err != nil {
		return nil, fmt.Errorf("plugin %s reading %s: %w", process.spec.ID, i.Pipeline, err)
	}
	var sets []facts.Set
	if err := json.Unmarshal(answer.GetFacts(), &sets); err != nil {
		return nil, fmt.Errorf("plugin %s reading %s: facts: %w", process.spec.ID, i.Pipeline, err)
	}
	return sets, nil
}

// NoHost is the Host of a program run outside an instance, by check-reading: it lends nothing.
func NoHost(string) pluginv1.HostServer { return pluginv1.UnimplementedHostServer{} }
