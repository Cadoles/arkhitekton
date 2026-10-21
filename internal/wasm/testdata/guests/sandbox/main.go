// Command sandbox is a test guest: it reports what it can reach of the host.
package main

import (
	"context"
	"os"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/guest"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

type reader struct{}

func (reader) Interpret(_ context.Context, run capability.RunState, records []parse.Record) ([]facts.Set, error) {
	_, fileErr := os.ReadFile("/etc/hostname")
	_, dirErr := os.ReadDir("/")
	reached := facts.Case{Suite: "sandbox", Name: "file " + errText(fileErr) + ", root " + errText(dirErr) +
		", home " + os.Getenv("HOME") + ", args " + strings.Join(os.Args, " "), Status: facts.CasePassed}
	return []facts.Set{{Shape: facts.ShapeCases, Group: "unit", Tool: "sandbox", Cases: []facts.Case{reached}}}, nil
}

func init() { guest.Register(reader{}) }

func main() {}

func errText(err error) string {
	if err == nil {
		return "read"
	}
	return "refused"
}
