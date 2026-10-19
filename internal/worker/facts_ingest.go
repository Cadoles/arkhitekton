package worker

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/parse"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Reading a finished run through the plugin that launched it.
 *
 * The capability says what the run publishes and how each file is parsed; the dashboard fetches
 * and parses, the plugin's interpreter turns records and the run's state into facts, and the
 * dashboard checks and stores them. The plugin never touches storage nor bytes it did not ask for.
 *
 * **Once, when everything is over.** The PipelineRun and its TaskRuns arrive in no guaranteed
 * order: reading as soon as the PipelineRun ends could take a task still being recorded for one
 * that never ran. The reading waits for every task, is triggered by both, and the run's mark makes
 * it happen once.
 */

// FactStore is what reading a run needs from the database.
type FactStore interface {
	RunSnapshot(ctx context.Context, runID int64) (store.RunSnapshot, error)
	GetJob(ctx context.Context, id int64) (store.Job, error)
	StartConditions(ctx context.Context, jobID int64) (bool, int, error)
	SaveFacts(ctx context.Context, run store.InterpretedRun) error
}

// FactIngester reads finished runs through their capability's interpreter.
type FactIngester struct {
	store    FactStore
	objects  Objects
	registry *capability.Registry
	logger   *slog.Logger
}

func NewFactIngester(
	facts FactStore, objects Objects, registry *capability.Registry, logger *slog.Logger,
) *FactIngester {
	return &FactIngester{store: facts, objects: objects, registry: registry, logger: logger}
}

// Ingest reads a run if it is over and was not read yet.
func (i *FactIngester) Ingest(ctx context.Context, runID int64) error {
	snapshot, err := i.store.RunSnapshot(ctx, runID)
	if err != nil {
		return err
	}
	if snapshot.Interpreted || !snapshot.Terminal() {
		return nil
	}

	ref, version, found, err := i.capabilityOf(ctx, snapshot)
	if err != nil || !found {
		// A run no capability reads (a Pipeline triggered outside the dashboard) has nothing to say.
		return err
	}
	pipeline, plugin, _ := i.registry.Pipeline(ref)

	run := store.InterpretedRun{
		RunID: snapshot.ID, ReleaseID: snapshot.ReleaseID,
		Producer: facts.Producer{Plugin: plugin.ID, Version: plugin.Version}, Capability: ref.String(),
	}
	if snapshot.JobID != nil {
		run.Exclusive, run.ConcurrentJobs, err = i.store.StartConditions(ctx, *snapshot.JobID)
		if err != nil {
			return err
		}
	}

	if pipeline.Interpreter != nil {
		if version != "" && version != plugin.Version {
			// A built-in plugin carries only its current interpretation. The run is read by it, and the
			// producer says so, rather than claiming the version that launched it.
			i.logger.Warn("run read by another plugin version than the one that launched it",
				"run", snapshot.Name, "capability", ref.String(), "launched", version, "reading", plugin.Version)
		}
		records := i.records(ctx, snapshot.Name, pipeline.Spec)
		sets, err := pipeline.Interpreter.Interpret(ctx, stateOf(snapshot, run), records)
		if err != nil {
			// An interpreter may return what it could read alongside what it could not.
			i.logger.Error("interpreting a run", "run", snapshot.Name, "capability", ref.String(), "error", err)
		}
		for _, set := range sets {
			if err := set.Validate(); err != nil {
				i.logger.Error("facts refused", "run", snapshot.Name, "capability", ref.String(),
					"tool", set.Tool, "error", err)
				continue
			}
			run.Sets = append(run.Sets, set)
		}
	}

	if err := i.store.SaveFacts(ctx, run); err != nil {
		if errors.Is(err, store.ErrAlreadyInterpreted) {
			return nil
		}
		return err
	}
	if len(run.Sets) > 0 {
		i.logger.Info("run read", "run", snapshot.Name, "capability", ref.String(),
			"producer", run.Producer.String(), "sets", len(run.Sets))
	}
	return nil
}

// capabilityOf finds what a run instantiates: the job's capability for a launched run, the
// observed Pipeline's for one the dashboard did not launch.
func (i *FactIngester) capabilityOf(
	ctx context.Context, snapshot store.RunSnapshot,
) (capability.Ref, string, bool, error) {
	if snapshot.JobID != nil {
		job, err := i.store.GetJob(ctx, *snapshot.JobID)
		if err != nil {
			return capability.Ref{}, "", false, err
		}
		name := job.Capability
		if name == "" && strings.Contains(job.Kind, "/") {
			name = job.Kind
		}
		if name == "" {
			return capability.Ref{}, "", false, nil
		}
		ref, err := capability.ParseRef(name)
		if err != nil {
			return capability.Ref{}, "", false, nil
		}
		_, _, found := i.registry.Pipeline(ref)
		return ref, job.PluginVersion, found, nil
	}
	ref, found := i.registry.ByPipelineRef(snapshot.Pipeline)
	return ref, "", found, nil
}

func stateOf(snapshot store.RunSnapshot, run store.InterpretedRun) capability.RunState {
	state := capability.RunState{
		Status: snapshot.Status, Reason: snapshot.Reason,
		StartedAt: snapshot.StartedAt, CompletedAt: snapshot.CompletedAt,
		Conditions: capability.Conditions{Exclusive: run.Exclusive, ConcurrentAtStart: run.ConcurrentJobs},
	}
	for _, task := range snapshot.Tasks {
		taskState := capability.TaskState{Name: task.Name, Status: task.Status, Reason: task.Reason}
		for _, step := range task.Steps {
			taskState.Steps = append(taskState.Steps,
				capability.StepState{Name: step.Name, Status: step.Status, ExitCode: step.ExitCode})
		}
		state.Tasks = append(state.Tasks, taskState)
	}
	return state
}

// records fetches and parses what the capability declares. A missing artifact is no record: the
// interpreter, which knows whether the task ran, decides what that means.
func (i *FactIngester) records(ctx context.Context, runName string, spec capability.PipelineSpec) []parse.Record {
	prefix := "raw/" + runName + "/"
	var listed []ObjectRef
	var listedOnce bool

	var records []parse.Record
	for _, artifact := range spec.Artifacts {
		var keys []string
		if strings.ContainsAny(artifact.Object, "*?[") {
			if !listedOnce {
				var err error
				listed, err = i.objects.List(ctx, prefix)
				if err != nil {
					i.logger.Error("artifacts of a run", "run", runName, "error", err)
				}
				listedOnce = true
			}
			for _, object := range listed {
				if matched, _ := path.Match(artifact.Object, strings.TrimPrefix(object.Key, prefix)); matched {
					keys = append(keys, object.Key)
				}
			}
		} else {
			keys = []string{prefix + artifact.Object}
		}

		for _, key := range keys {
			records = append(records, i.read(ctx, runName, key, artifact)...)
		}
	}
	return records
}

func (i *FactIngester) read(ctx context.Context, runName, key string, artifact capability.Artifact) []parse.Record {
	content, err := i.objects.Get(ctx, key)
	if err != nil {
		// Most often an artifact never published: the run stopped before, which the run's state says.
		i.logger.Debug("artifact absent", "run", runName, "object", key, "error", err)
		return nil
	}
	defer content.Close()

	if artifact.Format != capability.FormatArchive {
		member, wanted := memberFor(artifact, path.Base(key))
		if !wanted {
			return nil
		}
		raw, err := io.ReadAll(io.LimitReader(content, maxArtifactBytes+1))
		if err == nil && len(raw) > maxArtifactBytes {
			err = fmt.Errorf("beyond %d bytes", maxArtifactBytes)
		}
		return []parse.Record{parseRecord(artifact.Name, "", key, member, raw, err)}
	}

	var records []parse.Record
	err = unpack(content, func(name string, reader io.Reader, budget int64) (int64, error) {
		member, wanted := memberFor(artifact, lastTwo(name))
		if !wanted {
			return 0, nil
		}
		raw, err := io.ReadAll(io.LimitReader(reader, budget))
		records = append(records, parseRecord(artifact.Name, name, key, member, raw, err))
		return int64(len(raw)), err
	})
	if err != nil {
		i.logger.Error("archive of a run", "run", runName, "object", key, "error", err)
	}
	return records
}

// unpack walks a gzipped tar, handing each regular file to visit. Only the files visit reads count
// against the budget: an end-to-end archive weighs mostly videos nobody parses.
func unpack(source io.Reader, visit func(name string, reader io.Reader, budget int64) (int64, error)) error {
	gz, err := gzip.NewReader(source)
	if err != nil {
		return fmt.Errorf("unreadable archive: %w", err)
	}
	defer gz.Close()

	reader := tar.NewReader(gz)
	var budget int64 = maxArtifactBytes
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("unreadable archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		read, err := visit(header.Name, reader, budget)
		if err != nil {
			return err
		}
		budget -= read
		if budget <= 0 {
			return errors.New("decompressed size beyond the limit")
		}
	}
}

// lastTwo is a member's parent directory and name, what a member rule matches: the directory names
// the tool, wherever the archive roots it.
func lastTwo(name string) string {
	clean := path.Clean(strings.TrimPrefix(name, "./"))
	return path.Join(path.Base(path.Dir(clean)), path.Base(clean))
}

func memberFor(artifact capability.Artifact, name string) (capability.Member, bool) {
	for _, member := range artifact.Members {
		if matched, _ := path.Match(member.Match, name); matched {
			return member, true
		}
	}
	return capability.Member{}, false
}

func parseRecord(artifact, member, object string, rule capability.Member, raw []byte, err error) parse.Record {
	record := parse.Record{Artifact: artifact, Member: member, Object: object, Parser: rule.Parser, Tool: rule.Tool}
	if err != nil {
		record.Err = err
		return record
	}
	parser, known := parse.Lookup(rule.Parser)
	if !known {
		record.Err = fmt.Errorf("no parser %q", rule.Parser)
		return record
	}
	record.Payload, record.Err = parser(raw)
	return record
}
