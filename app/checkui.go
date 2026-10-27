package app

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/installed"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/ui"
)

// runCheckUI checks that a plugin's interface composes with the core's, as its installation will,
// and prints the interface it composes to with --print.
func runCheckUI(args []string) error {
	flags := flag.NewFlagSet("check-ui", flag.ContinueOnError)
	dir := flags.String("plugin", "", "the plugin's directory")
	printed := flags.Bool("print", false, "prints the composed interface, everything shown")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return errors.New("usage: arkhitekton check-ui --plugin <dir> [--print]")
	}
	contribution, err := installed.InterfaceOf(os.DirFS(*dir))
	if err != nil {
		return err
	}
	if err := ui.Check(ui.Core(), nil, contribution, interfaceOptions); err != nil {
		return err
	}
	if !*printed {
		fmt.Printf("%s: %d resource(s) compose with the core's\n", contribution.Plugin, len(contribution.Resources))
		return nil
	}
	composed, _ := ui.Compose(ui.Core(), []ui.Contribution{contribution}, interfaceOptions)
	everything := ui.Viewer{Active: func(string) bool { return true }, Can: func(string) bool { return true }}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(composed.For(everything))
}
