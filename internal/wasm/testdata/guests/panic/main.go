// Command panic is a test guest: it panics.
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
	var cases []facts.Case
	_ = cases[len(records)+3]
	return nil, nil
}

func init() { guest.Register(reader{}) }

func main() {}
