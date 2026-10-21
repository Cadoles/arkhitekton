package wasm

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

/*
 * The sandbox, against guests built for the occasion with the standard Go toolchain.
 */

// guests holds the modules built from testdata/guests, by name.
var guests = map[string][]byte{}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "arkhitekton-guests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	entries, _ := os.ReadDir(filepath.Join("testdata", "guests"))
	for _, entry := range entries {
		out := filepath.Join(dir, entry.Name()+".wasm")
		build := exec.Command("go", "build", "-buildmode=c-shared", "-o", out, "./testdata/guests/"+entry.Name())
		build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
		if output, err := build.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "building guest %s: %v\n%s", entry.Name(), err, output)
			os.Exit(1)
		}
		guests[entry.Name()], _ = os.ReadFile(out)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func load(t *testing.T, name string, limits Limits) *Interpreter {
	t.Helper()
	interpreter, err := Load(context.Background(), name, guests[name], limits, quiet())
	if err != nil {
		t.Fatalf("loading %s: %v", name, err)
	}
	t.Cleanup(func() { _ = interpreter.Close(context.Background()) })
	return interpreter
}

func TestAModuleReadsARun(t *testing.T) {
	interpreter := load(t, "echo", DefaultLimits)
	sets, err := interpreter.Interpret(context.Background(), capability.RunState{Status: "succeeded"}, []parse.Record{
		{Tool: "gotest", Parser: parse.Raw, Payload: parse.Bytes("first")},
		{Tool: "gotest", Parser: parse.Raw, Payload: parse.Bytes("second")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 || sets[0].Summary != "succeeded" || len(sets[0].Cases) != 2 || sets[0].Cases[1].Name != "second" {
		t.Errorf("sets = %+v", sets)
	}

	// A fresh instance per run: nothing of the first run survives into the second.
	sets, err = interpreter.Interpret(context.Background(), capability.RunState{}, nil)
	if err != nil || len(sets) != 1 || len(sets[0].Cases) != 0 {
		t.Errorf("second run: %+v, %v", sets, err)
	}
}

// Every way a module can go wrong ends in a failed reading, and the worker keeps going.
func TestAFailingModuleFailsItsReadingOnly(t *testing.T) {
	short := DefaultLimits
	short.Duration = 2 * time.Second
	for name, expected := range map[string]string{
		"panic":  "panicked",
		"loop":   "no answer within",
		"memory": "",
		"huge":   "beyond",
	} {
		started := time.Now()
		_, err := load(t, name, short).Interpret(context.Background(), capability.RunState{}, nil)
		if err == nil || !strings.Contains(err.Error(), expected) {
			t.Errorf("%s: %v", name, err)
		}
		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Errorf("%s: took %s", name, elapsed)
		}
	}
}

// The module sees no file, no directory, no environment and no argument of the host.
func TestTheSandboxIsEmpty(t *testing.T) {
	sets, err := load(t, "sandbox", DefaultLimits).Interpret(context.Background(), capability.RunState{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reached := sets[0].Cases[0].Name; reached != "file refused, root refused, home , args " {
		t.Errorf("the module reached %q", reached)
	}
}

// What a module prints is kept for its author, bounded, and does not disturb its answer.
func TestAChattyModuleIsBounded(t *testing.T) {
	limits := DefaultLimits
	limits.ConsoleBytes = 1024
	sets, err := load(t, "chatty", limits).Interpret(context.Background(), capability.RunState{}, nil)
	if err != nil || len(sets) != 1 || sets[0].Summary != "read" {
		t.Errorf("sets %+v, err %v", sets, err)
	}
}

// What would let a module out of its sandbox, or misread a run, is refused when it is loaded.
func TestAnUnfitModuleIsRefusedAtLoad(t *testing.T) {
	// A module importing env.f: the smallest that asks the host for a function it does not offer.
	foreign := []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, // magic, version
		0x01, 0x04, 0x01, 0x60, 0x00, 0x00, // one type: func()
		0x02, 0x09, 0x01, 0x03, 'e', 'n', 'v', 0x01, 'f', 0x00, 0x00, // import env.f
	}
	small := DefaultLimits
	small.ModuleBytes = 1024
	for label, module := range map[string][]byte{
		"foreign import": foreign,
		"not wasm":       []byte("#!/bin/sh"),
		"too large":      guests["echo"],
	} {
		limits := DefaultLimits
		if label == "too large" {
			limits = small
		}
		if _, err := Load(context.Background(), label, module, limits, quiet()); err == nil {
			t.Errorf("%s: accepted", label)
		}
	}
}
