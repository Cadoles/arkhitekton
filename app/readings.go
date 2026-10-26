package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/declarative"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/installed"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/native"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
)

// runCheckReading checks a declared reading, a module compiled to WebAssembly, or a plugin's
// program, against recorded cases, without a database nor a cluster: what a plugin author runs
// before shipping a reading. With --write, the facts produced become the expected ones of the
// cases that record none.
func runCheckReading(args []string) error {
	flags := flag.NewFlagSet("check-reading", flag.ContinueOnError)
	write := flags.Bool("write", false, "records the facts produced as expected.json where it is missing")
	pluginDir := flags.String("plugin", "", "a plugin's directory whose program reads the cases")
	pipeline := flags.String("pipeline", "", "the pipeline the program reads, when it reads several")
	if err := flags.Parse(args); err != nil {
		return err
	}

	var check func(dir string) (got, expected []byte, err error)
	cases := flags.Args()
	if *pluginDir != "" {
		program, stop, err := pluginReading(*pluginDir, *pipeline)
		if err != nil {
			return err
		}
		defer stop()
		check = func(dir string) ([]byte, []byte, error) { return declarative.CheckWith(program, dir) }
	} else {
		if flags.NArg() < 2 {
			return errors.New("usage: arkhitekton check-reading [--write] <reading.yaml|module.wasm> <case>...\n" +
				"       arkhitekton check-reading [--write] --plugin <dir> [--pipeline <name>] <case>...")
		}
		reading, err := os.ReadFile(flags.Arg(0))
		if err != nil {
			return err
		}
		check = func(dir string) ([]byte, []byte, error) { return declarative.Check(reading, dir) }
		cases = cases[1:]
	}
	if len(cases) == 0 {
		return errors.New("no case to check")
	}

	failed := 0
	for _, dir := range cases {
		got, expected, err := check(dir)
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
		return fmt.Errorf("%d case(s) out of %d do not read as expected", failed, len(cases))
	}
	return nil
}

// pluginReading launches a plugin's program from its directory, the one built for this machine,
// lent nothing of an instance, and returns the interpreter of the pipeline it reads.
func pluginReading(dir, pipeline string) (native.Interpreter, func(), error) {
	declared, err := installed.ReadManifest(os.DirFS(dir))
	if err != nil {
		return native.Interpreter{}, nil, err
	}
	if declared.Binary == "" {
		return native.Interpreter{}, nil, fmt.Errorf("%s: the plugin has no program", dir)
	}
	if pipeline == "" {
		for _, declaredPipeline := range declared.Pipelines {
			if declaredPipeline.Interpreter != manifest.InterpreterNative {
				continue
			}
			if pipeline != "" {
				return native.Interpreter{}, nil, errors.New("the program reads several pipelines: name one with --pipeline")
			}
			pipeline = declaredPipeline.Name
		}
		if pipeline == "" {
			return native.Interpreter{}, nil, fmt.Errorf("%s: the program reads no pipeline", dir)
		}
	}

	path := filepath.Join(dir, declared.Binary)
	if _, err := os.Stat(path); err != nil {
		path += "-" + runtime.GOOS + "-" + runtime.GOARCH
	}
	file, err := os.Open(path)
	if err != nil {
		return native.Interpreter{}, nil, fmt.Errorf("the plugin's program, built for this machine: %w", err)
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	_ = file.Close()
	if err != nil {
		return native.Interpreter{}, nil, err
	}

	work, err := os.MkdirTemp("", "arkhitekton-check-")
	if err != nil {
		return native.Interpreter{}, nil, err
	}
	launcher := native.New(native.Config{
		Dir: work, Role: rpc.RoleReprocess, Host: native.NoHost,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	process := launcher.Process(native.Spec{
		ID: declared.ID, Version: declared.Version, Digest: "sha256:" + hex.EncodeToString(hash.Sum(nil)),
		Binary: func() (io.ReadCloser, error) { return os.Open(path) },
	})
	stop := func() {
		launcher.Close()
		_ = os.RemoveAll(work)
	}
	return native.Interpreter{Process: func() (*native.Process, error) { return process, nil }, Pipeline: pipeline}, stop, nil
}
