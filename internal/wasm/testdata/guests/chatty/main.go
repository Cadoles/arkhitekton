// Command chatty is a test guest: it prints a lot while reading.
package main

import (
	"context"
	"fmt"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/guest"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

type reader struct{}

func (reader) Interpret(_ context.Context, run capability.RunState, records []parse.Record) ([]facts.Set, error) {
	for i := 0; i < 10000; i++ {
		fmt.Println("reading line", i)
	}
	return []facts.Set{{Shape: facts.ShapeCases, Group: "unit", Tool: "chatty", Status: facts.StatusPass, Summary: "read"}}, nil
}

func init() { guest.Register(reader{}) }

func main() {}
