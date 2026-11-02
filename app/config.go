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
	"time"

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
		return errors.New("config: expected a subcommand (export, diff, apply)")
	}
	switch args[0] {
	case "export":
		return runConfigExport(ctx, cfg, args[1:], os.Stdout, os.Stderr)
	case "diff":
		return runConfigApply(ctx, cfg, logger, append([]string{"--dry-run"}, args[1:]...), os.Stdin, os.Stdout, os.Stderr)
	case "apply":
		return runConfigApply(ctx, cfg, logger, args[1:], os.Stdin, os.Stdout, os.Stderr)
	default:
		return fmt.Errorf("config: unknown subcommand %q (expected: export, diff, apply)", args[0])
	}
}

func runConfigApply(
	ctx context.Context, cfg config.Config, logger *slog.Logger, args []string, stdin io.Reader, stdout, stderr io.Writer,
) error {
	flags := flag.NewFlagSet("config apply", flag.ContinueOnError)
	path := flags.String("f", "", "a file, a directory, or - for the standard input")
	dryRun := flags.Bool("dry-run", false, "print the operations without writing anything")
	prune := flags.Bool("prune", false, "also remove what the declared projects and the instance no longer declare")
	actor := flags.String("actor", "config-apply", "the name the audit log records the writes under")
	wait := flags.Duration("wait-timeout", 10*time.Minute, "how long a declared plugin's installation may take")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		return errors.New("config apply: -f is required (a file, a directory, or -)")
	}
	desired, err := configfile.ReadPath(*path, stdin)
	if err != nil {
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
	cipher, err := openCipher(cfg, logger)
	if err != nil {
		return err
	}
	sources := newCapabilitySources(cfg, db, storage, nil)

	// The declared plugins are installed first: the rest of the configuration may name what they
	// bring, and nothing of it can be checked before they are there.
	installer := configure.Installer{
		Installations: configure.Installations{Store: db, Check: sources.checkActivation, Logger: logger},
		Store:         db, Actor: *actor, Wait: *wait,
	}
	installs, err := installer.Installs(ctx, desired)
	if err != nil {
		return err
	}
	for _, op := range installs {
		fmt.Fprintln(stdout, op.Description)
	}
	if len(installs) > 0 {
		if *dryRun {
			fmt.Fprintf(stdout, "%d installation(s), nothing written (dry run): the rest of the configuration "+
				"is checked once %s installed.\n", len(installs), strings.Join(configure.PendingPlugins(installs), ", "))
			return nil
		}
		if err := (configure.Applier{}).Execute(ctx, installs); err != nil {
			return fmt.Errorf("stopped: %w", err)
		}
	}

	snapshot, err := sources.snapshot(ctx, 0)
	if err != nil {
		return err
	}
	registry, pipelines := snapshot.Registry, snapshot.Catalog

	// The same services the screen writes through, given what the API gives them. The deployer's
	// scheme check is left out: it is a hook of a running plugin, and the screen still applies it.
	services := configure.Services{
		Projects: configure.Projects{
			Store: db, ForgeURL: cfg.Forge.URL, ImageRegistry: cfg.Registry.Project,
			Library: referenceLibrary(), Logger: logger,
		},
		Plugins:          configure.Plugins{Store: db, Registry: registry, Cipher: cipher, Logger: logger},
		ProjectPipelines: configure.ProjectPipelines{Store: db, Catalog: pipelines, Logger: logger},
		QueueLimits:      configure.QueueLimits{Store: db, Logger: logger},
		Bindings: configure.Bindings{
			Store: db, Registry: registry, Catalog: pipelines, Check: sources.checkBinding, Logger: logger,
		},
		StatusMappings: configure.StatusMappings{Store: db, Logger: logger},
		Automations: configure.Automations{
			Store: db, Schedules: db, Catalog: pipelines, Registry: registry, Logger: logger,
		},
	}
	applier := configure.Applier{
		Services: services, Store: db, Registry: registry, Catalog: pipelines, Actor: *actor,
	}

	ops, warnings, err := applier.Plan(ctx, desired, *prune)
	for _, warning := range warnings {
		fmt.Fprintln(stderr, "warning:", warning)
	}
	if err != nil {
		return fmt.Errorf("nothing written:\n%w", err)
	}
	if len(ops) == 0 {
		if len(installs) == 0 {
			fmt.Fprintln(stdout, "The instance already matches the configuration.")
		}
		return nil
	}
	for _, op := range ops {
		fmt.Fprintln(stdout, op.Description)
	}
	if *dryRun {
		fmt.Fprintf(stdout, "%d operation(s), nothing written (dry run).\n", len(ops))
		return nil
	}
	if err := applier.Execute(ctx, ops); err != nil {
		return fmt.Errorf("stopped: %w (applying again resumes from there)", err)
	}
	fmt.Fprintf(stdout, "%d operation(s) written.\n", len(ops))
	return nil
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
