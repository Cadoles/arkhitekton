package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/blob"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/configfile"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/configure"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The instance's configuration as versioned files.
 *
 * Run inside the worker's pod (`kubectl exec`): it has the database, the object store holding the
 * installed plugins, the catalogue and the sealing key, and nothing has to be deployed for it.
 * YAML goes to the standard output, everything else to the error output, so that the output can be
 * redirected into a file.
 */

func runConfig(ctx context.Context, cfg config.Config, logger *slog.Logger, args []string) error {
	if len(args) == 0 {
		return errors.New("config: expected a subcommand (export)")
	}
	switch args[0] {
	case "export":
		return runConfigExport(ctx, cfg, args[1:], os.Stdout, os.Stderr)
	default:
		return fmt.Errorf("config: unknown subcommand %q (expected: export)", args[0])
	}
}

func runConfigExport(ctx context.Context, cfg config.Config, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("config export", flag.ContinueOnError)
	dir := flags.String("o", "", "write a directory (instance.yaml, projects/<slug>.yaml) instead of the standard output")
	if err := flags.Parse(args); err != nil {
		return err
	}

	db, err := store.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	storage, err := blob.Open(ctx, cfg.Storage)
	if err != nil {
		return err
	}
	// The installed plugins are read without starting their programs: the export needs their
	// descriptors, to know which settings are sensitive, not their code.
	registry, err := newCapabilitySources(cfg, db, storage, nil).registry(ctx, nil)
	if err != nil {
		return err
	}

	exported, err := configure.Exporter{Store: db, Registry: registry}.Export(ctx)
	if err != nil {
		return err
	}
	for _, warning := range exported.Warnings {
		fmt.Fprintln(stderr, "warning:", warning)
	}
	if len(exported.Secrets) > 0 {
		fmt.Fprintln(stderr, "sensitive settings are read from:", strings.Join(exported.Secrets, ", "))
	}

	if *dir != "" {
		return configfile.WriteDir(*dir, exported.Set)
	}
	raw, err := configfile.Encode(exported.Set)
	if err != nil {
		return err
	}
	_, err = stdout.Write(raw)
	return err
}
