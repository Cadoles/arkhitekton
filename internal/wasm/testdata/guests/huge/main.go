// Command huge is a test guest: its answer is enormous.
package main

import (
	"context"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/guest"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

type reader struct{}

func (reader) Interpret(_ context.Context, run capability.RunState, records []parse.Record) ([]facts.Set, error) {
	summary := strings.Repeat("x", 20<<20)
	return []facts.Set{{Shape: facts.ShapeCases, Group: "unit", Tool: "huge", Summary: summary}}, nil
}

func init() { guest.Register(reader{}) }

func main() {}
