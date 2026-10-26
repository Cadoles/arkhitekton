package worker

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/blob"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/parsers"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
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
	registry capabilities.Source
	logger   *slog.Logger
}

func NewFactIngester(
	facts FactStore, objects Objects, registry capabilities.Source, logger *slog.Logger,
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
	pipeline, plugin, _ := capabilities.Of(i.registry).PipelineAt(ref, version)

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
			// A built-in plugin carries only its current interpretation, and an installed one its
			// versions still installed. The run is read by the current one, and the producer says so,
			// rather than claiming the version that launched it.
			i.logger.Warn("run read by another plugin version than the one that launched it",
				"run", snapshot.Name, "capability", ref.String(), "launched", version, "reading", plugin.Version)
		}
		records := i.records(ctx, snapshot.Name, pipeline.Spec)
		sets, err := pipeline.Interpreter.Interpret(ctx, stateOf(snapshot, run), records)
		if err != nil {
			// An interpreter may return what it could read alongside what it could not.
			i.logger.Error("interpreting a run", "run", snapshot.Name, "capability", ref.String(), "error", err)
		}
		kept := attachedObjects(records)
		for _, set := range sets {
			refused := set.Validate()
			if refused == nil {
				refused = attachedOnly(set, kept)
			}
			if refused != nil {
				i.logger.Error("facts refused", "run", snapshot.Name, "capability", ref.String(),
					"tool", set.Tool, "error", refused)
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
		_, _, found := capabilities.Of(i.registry).PipelineAt(ref, job.PluginVersion)
		return ref, job.PluginVersion, found, nil
	}
	ref, found := capabilities.Of(i.registry).ByPipelineRef(snapshot.Pipeline)
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
	quota := &attachmentQuota{}
	for _, artifact := range spec.Artifacts {
		if len(artifact.Members) == 0 {
			// Nothing in it is read nor kept: downloading and decompressing it would be for nothing.
			continue
		}
		var keys []ObjectRef
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
					keys = append(keys, object)
				}
			}
		} else {
			keys = []ObjectRef{{Key: prefix + artifact.Object}}
		}

		for _, key := range keys {
			records = append(records, i.read(ctx, runName, key, artifact, quota)...)
		}
	}
	return records
}

func (i *FactIngester) read(
	ctx context.Context, runName string, object ObjectRef, artifact capability.Artifact, quota *attachmentQuota,
) []parse.Record {
	key := object.Key
	if artifact.Format != capability.FormatArchive {
		// A plain file kept as an attachment is already stored: it is attached where it is, unread.
		if member, wanted := memberFor(artifact, path.Base(key)); wanted && member.Parser == parse.Attach {
			return []parse.Record{{
				Artifact: artifact.Name, Object: key, Parser: parse.Attach, Tool: member.Tool,
				Payload: parse.Attached{Object: key, MediaType: mediaTypeOf(member, key, nil), Size: object.Size},
			}}
		}
	}

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
	err = unpack(content, func(name string, size int64, reader io.Reader, budget int64) (int64, error) {
		member, wanted := memberFor(artifact, lastTwo(name))
		if !wanted {
			return 0, nil
		}
		if member.Parser == parse.Attach {
			records = append(records, i.keep(ctx, runName, key, artifact, member, name, size, reader, quota))
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
func unpack(source io.Reader, visit func(name string, size int64, reader io.Reader, budget int64) (int64, error)) error {
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
		read, err := visit(header.Name, header.Size, reader, budget)
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
	parser, known := parsers.Lookup(rule.Parser)
	if !known {
		record.Err = fmt.Errorf("no parser %q", rule.Parser)
		return record
	}
	record.Payload, record.Err = parser(raw)
	return record
}

// The bounds of what a run may keep as attachments. Videos of an end-to-end campaign weigh tens of
// megabytes each; a runaway loop recording thousands must end in a message, not in a full bucket.
const (
	maxAttachmentBytes = 256 << 20
	maxAttachedBytes   = 2 << 30
	maxAttachments     = 500
)

// attachmentQuota counts what one run has kept so far.
type attachmentQuota struct {
	count int
	bytes int64
}

// keep stores an archive member as an object of its own, streamed without being loaded, and hands
// over where it is.
func (i *FactIngester) keep(
	ctx context.Context, runName, archive string, artifact capability.Artifact, member capability.Member,
	name string, size int64, reader io.Reader, quota *attachmentQuota,
) parse.Record {
	record := parse.Record{Artifact: artifact.Name, Member: name, Object: archive, Parser: parse.Attach, Tool: member.Tool}
	switch {
	case size > maxAttachmentBytes:
		record.Err = fmt.Errorf("%d bytes, beyond %d for one attachment", size, maxAttachmentBytes)
		return record
	case quota.count >= maxAttachments || quota.bytes+size > maxAttachedBytes:
		record.Err = fmt.Errorf("beyond what one run may keep (%d files, %d bytes)", maxAttachments, maxAttachedBytes)
		return record
	}

	key, err := blob.Keys{}.Attachment(runName, name)
	if err != nil {
		record.Err = err
		return record
	}
	buffered := bufio.NewReaderSize(reader, 512)
	head, _ := buffered.Peek(512)
	mediaType := mediaTypeOf(member, name, head)
	if err := i.objects.Put(ctx, key, buffered, size, mediaType); err != nil {
		record.Err = err
		return record
	}
	quota.count++
	quota.bytes += size
	record.Payload = parse.Attached{Object: key, MediaType: mediaType, Size: size}
	return record
}

// mediaTypeOf is the member rule's type, else the name's, else what the first bytes look like.
func mediaTypeOf(member capability.Member, name string, head []byte) string {
	if member.MediaType != "" {
		return member.MediaType
	}
	if byName := mime.TypeByExtension(path.Ext(name)); byName != "" {
		return byName
	}
	if len(head) > 0 {
		return http.DetectContentType(head)
	}
	return "application/octet-stream"
}

// attachedObjects are the objects the host kept for this run: the only ones an attachment may name.
func attachedObjects(records []parse.Record) map[string]bool {
	kept := map[string]bool{}
	for _, record := range records {
		if attached, ok := record.Payload.(parse.Attached); ok && record.Err == nil {
			kept[attached.Object] = true
		}
	}
	return kept
}

// attachedOnly refuses a set attaching an object the host did not keep for this run. Without it, an
// interpreter could have the dashboard serve any object of the bucket under a release.
func attachedOnly(set facts.Set, kept map[string]bool) error {
	for _, attachment := range set.Attachments {
		if !kept[attachment.Object] {
			return fmt.Errorf("attachment %q names an object this run did not keep", attachment.Name)
		}
	}
	return nil
}
