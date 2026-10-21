// Package wasm runs the run interpreters plugins compile to WebAssembly.
//
// The module runs in a sandbox: an empty WASI, with no directory, no environment variable, no
// argument and no network, and no other host function; bounded memory, time, output and console.
// It receives the run's state and its parsed records, and returns facts the dashboard checks like
// any interpreter's. The exchange is sdk/guest's.
package wasm

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/guest"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// Limits bound what a module may take.
type Limits struct {
	ModuleBytes  int
	MemoryPages  uint32
	Duration     time.Duration
	OutputBytes  int
	ConsoleBytes int
}

// DefaultLimits are the dashboard's: generous for reading a report, short of what would hurt the
// worker.
var DefaultLimits = Limits{
	ModuleBytes:  32 << 20,
	MemoryPages:  1024, // 64 MiB
	Duration:     10 * time.Second,
	OutputBytes:  16 << 20,
	ConsoleBytes: 64 << 10,
}

// The exports a module must offer.
const (
	exportABI       = "arkhitekton_abi"
	exportAlloc     = "arkhitekton_alloc"
	exportInterpret = "arkhitekton_interpret"
)

// The one host module a module may import, provided empty.
const wasiModule = wasi_snapshot_preview1.ModuleName

// Interpreter is a module ready to read runs.
type Interpreter struct {
	name     string
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	limits   Limits
	logger   *slog.Logger
}

var _ capability.Interpreter = (*Interpreter)(nil)

// Load checks a module and compiles it: its size, its imports, its exports and the version of the
// exchange it speaks. What is refused here is refused when the plugin is loaded, not on a run.
func Load(ctx context.Context, name string, module []byte, limits Limits, logger *slog.Logger) (*Interpreter, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if len(module) > limits.ModuleBytes {
		return nil, fmt.Errorf("module of %d bytes, beyond %d", len(module), limits.ModuleBytes)
	}
	runtime := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithMemoryLimitPages(limits.MemoryPages).
		WithCloseOnContextDone(true))
	compiled, err := runtime.CompileModule(ctx, module)
	if err != nil {
		_ = runtime.Close(ctx)
		return nil, fmt.Errorf("compiling the module: %w", err)
	}
	interpreter := &Interpreter{name: name, runtime: runtime, compiled: compiled, limits: limits, logger: logger}
	if err := interpreter.check(ctx); err != nil {
		_ = runtime.Close(ctx)
		return nil, err
	}
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		_ = runtime.Close(ctx)
		return nil, err
	}

	// The version is asked of a running instance: it is the one answer that proves the module
	// speaks the exchange, not merely names its functions.
	instance, _, err := interpreter.instantiate(ctx)
	if err != nil {
		_ = runtime.Close(ctx)
		return nil, err
	}
	defer instance.Close(ctx)
	results, err := instance.ExportedFunction(exportABI).Call(ctx)
	if err != nil {
		_ = runtime.Close(ctx)
		return nil, fmt.Errorf("asking the module its ABI: %w", err)
	}
	if version := int32(results[0]); version != guest.ABI {
		_ = runtime.Close(ctx)
		return nil, fmt.Errorf("the module speaks ABI %d, the dashboard %d", version, guest.ABI)
	}
	return interpreter, nil
}

func (i *Interpreter) check(ctx context.Context) error {
	var problems []error
	for _, imported := range i.compiled.ImportedFunctions() {
		if module, name, _ := imported.Import(); module != wasiModule {
			problems = append(problems, fmt.Errorf("the module imports %s.%s: only %s is provided", module, name, wasiModule))
		}
	}
	for _, imported := range i.compiled.ImportedMemories() {
		module, name, _ := imported.Import()
		problems = append(problems, fmt.Errorf("the module imports memory %s.%s: it must bring its own", module, name))
	}
	exported := i.compiled.ExportedFunctions()
	for _, name := range []string{exportABI, exportAlloc, exportInterpret} {
		if _, found := exported[name]; !found {
			problems = append(problems, fmt.Errorf("the module exports no %s", name))
		}
	}
	return errors.Join(problems...)
}

// console keeps the first bytes a module prints, and counts the rest.
type console struct {
	mu      sync.Mutex
	limit   int
	kept    bytes.Buffer
	dropped int
}

func (c *console) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	room := c.limit - c.kept.Len()
	if room <= 0 {
		c.dropped += len(p)
		return len(p), nil
	}
	if len(p) > room {
		c.kept.Write(p[:room])
		c.dropped += len(p) - room
		return len(p), nil
	}
	return c.kept.Write(p)
}

// instantiate starts a fresh instance: nothing passes from one run to the next.
func (i *Interpreter) instantiate(ctx context.Context) (api.Module, *console, error) {
	output := &console{limit: i.limits.ConsoleBytes}
	config := wazero.NewModuleConfig().
		// Anonymous: several runs may be read at once.
		WithName("").
		WithStdout(output).WithStderr(output).
		// The Go runtime needs clocks and randomness; it gets the real ones, and nothing else.
		WithSysWalltime().WithSysNanotime().WithRandSource(rand.Reader).
		WithStartFunctions("_initialize")
	instance, err := i.runtime.InstantiateModule(ctx, i.compiled, config)
	if err != nil {
		return nil, nil, fmt.Errorf("starting the module: %w", err)
	}
	return instance, output, nil
}

// Interpret reads a run in a fresh instance of the module.
func (i *Interpreter) Interpret(ctx context.Context, run capability.RunState, records []parse.Record) ([]facts.Set, error) {
	input := guest.Input{ABI: guest.ABI, Run: run}
	for _, record := range records {
		wire, err := guest.EncodeRecord(record)
		if err != nil {
			return nil, err
		}
		input.Records = append(input.Records, wire)
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, i.limits.Duration)
	defer cancel()
	instance, output, err := i.instantiate(ctx)
	if err != nil {
		return nil, err
	}
	defer instance.Close(context.Background())
	defer i.report(output)

	raw, err := i.exchange(ctx, instance, encoded)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("module %s: no answer within %s", i.name, i.limits.Duration)
		}
		return nil, fmt.Errorf("module %s: %w", i.name, err)
	}

	var answer guest.Output
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, fmt.Errorf("module %s: unreadable answer: %w", i.name, err)
	}
	if answer.Error != "" {
		return answer.Sets, fmt.Errorf("module %s: %s", i.name, answer.Error)
	}
	return answer.Sets, nil
}

func (i *Interpreter) exchange(ctx context.Context, instance api.Module, input []byte) ([]byte, error) {
	results, err := instance.ExportedFunction(exportAlloc).Call(ctx, uint64(len(input)))
	if err != nil {
		return nil, err
	}
	pointer := uint32(results[0])
	if !instance.Memory().Write(pointer, input) {
		return nil, errors.New("the module's buffer does not hold the input")
	}
	results, err = instance.ExportedFunction(exportInterpret).Call(ctx, uint64(pointer), uint64(len(input)))
	if err != nil {
		return nil, err
	}
	location := results[0]
	start, length := uint32(location>>32), uint32(location)
	if int(length) > i.limits.OutputBytes {
		return nil, fmt.Errorf("an answer of %d bytes, beyond %d", length, i.limits.OutputBytes)
	}
	raw, ok := instance.Memory().Read(start, length)
	if !ok {
		return nil, errors.New("the answer lies outside the module's memory")
	}
	// Copied: the instance's memory goes with it.
	return bytes.Clone(raw), nil
}

// report logs what the module printed. A module's console is for its author, not for the facts.
func (i *Interpreter) report(output *console) {
	if output.kept.Len() == 0 {
		return
	}
	i.logger.Info("module printed", "module", i.name, "output", output.kept.String(), "dropped_bytes", output.dropped)
}

// Close releases the compiled module.
func (i *Interpreter) Close(ctx context.Context) error { return i.runtime.Close(ctx) }
