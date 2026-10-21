// Command echo is a test guest: one case per record, named after its bytes.
package main

import (
	"context"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/guest"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

type reader struct{}

func (reader) Interpret(_ context.Context, run capability.RunState, records []parse.Record) ([]facts.Set, error) {
	set := facts.Set{Shape: facts.ShapeCases, Group: "unit", Tool: "echo", Summary: run.Status}
	for _, record := range records {
		if raw, ok := record.Payload.(parse.Bytes); ok {
			set.Cases = append(set.Cases, facts.Case{Suite: record.Tool, Name: string(raw), Status: facts.CasePassed})
		}
	}
	return []facts.Set{set}, nil
}

func init() { guest.Register(reader{}) }

func main() {}
