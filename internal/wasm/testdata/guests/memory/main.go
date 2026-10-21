// Command memory is a test guest: it allocates far beyond its limit.
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
	var hoard [][]byte
	for i := 0; i < 1<<20; i++ {
		hoard = append(hoard, make([]byte, 1<<20))
	}
	return nil, nil
}

func init() { guest.Register(reader{}) }

func main() {}
