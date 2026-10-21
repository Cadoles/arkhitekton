package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/declarative"
)

// runCheckReading checks a declared reading, or a module compiled to WebAssembly, against recorded
// cases, without a database nor a
// cluster: what a plugin author runs before shipping a reading. With --write, the facts produced
// become the expected ones of the cases that record none.
func runCheckReading(args []string) error {
	flags := flag.NewFlagSet("check-reading", flag.ContinueOnError)
	write := flags.Bool("write", false, "records the facts produced as expected.json where it is missing")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() < 2 {
		return errors.New("usage: arkhitekton check-reading [--write] <reading.yaml|module.wasm> <case>...")
	}
	reading, err := os.ReadFile(flags.Arg(0))
	if err != nil {
		return err
	}

	failed := 0
	for _, dir := range flags.Args()[1:] {
		got, expected, err := declarative.Check(reading, dir)
		switch {
		case err != nil:
			return err
		case expected == nil && *write:
			if err := os.WriteFile(filepath.Join(dir, "expected.json"), append(got, '\n'), 0o644); err != nil {
				return err
			}
			fmt.Printf("recorded  %s\n", dir)
		case expected == nil:
			fmt.Printf("no expected.json in %s; produced:\n%s\n", dir, got)
			failed++
		case declarative.Same(got, expected):
			fmt.Printf("ok        %s\n", dir)
		default:
			fmt.Printf("DIFFERS   %s\nexpected:\n%s\nproduced:\n%s\n", dir, expected, got)
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d case(s) out of %d do not read as expected", failed, flags.NArg()-1)
	}
	return nil
}
