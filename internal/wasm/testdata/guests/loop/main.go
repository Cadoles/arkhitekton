// Command loop is a test guest: it never answers.
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
	for {
		_ = run
	}
}

func init() { guest.Register(reader{}) }

func main() {}
