// Package blob carries access to the object store. The layout is described in
// docs/08-data-model.md.
package blob

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Prefixes of the dashboard's bucket. The layout is decided here and nowhere else:
// that is what lets the dashboard know the exact key of every artifact without
// searching by prefix.
const (
	prefixRaw       = "raw"
	prefixLogs      = "logs"
	prefixArtifacts = "artifacts"
)

// safeSegment accepts only what legitimately makes up a PipelineRun name, a release
// tag or a file name.
var safeSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ErrUnsafeSegment reports a refused key segment.
type ErrUnsafeSegment struct {
	Segment string
	Reason  string
}

func (e ErrUnsafeSegment) Error() string {
	return fmt.Sprintf("key segment refused %q: %s", e.Segment, e.Reason)
}

// checkSegment validates one key component.
//
// Segments come from PipelineRun names and release tags, the latter typed by a
// user. A tag reading "../../artifacts" would write outside the intended prefix —
// S3 has no notion of directory to object, the key is a free string and "a/../b"
// literally names an object so called. Validation is therefore the only barrier.
func checkSegment(segment string) error {
	switch {
	case segment == "":
		return ErrUnsafeSegment{segment, "vide"}
	case strings.Contains(segment, "/"):
		return ErrUnsafeSegment{segment, "contains a slash"}
	case segment == "." || segment == "..":
		return ErrUnsafeSegment{segment, "directory reference"}
	case !safeSegment.MatchString(segment):
		return ErrUnsafeSegment{segment, "characters not allowed"}
	}
	return nil
}

// join assembles a key after validating every segment.
func join(segments ...string) (string, error) {
	for _, segment := range segments {
		if err := checkSegment(segment); err != nil {
			return "", err
		}
	}
	return path.Join(segments...), nil
}

// Keys builds the bucket's keys. The type exists so knowledge of the layout stays in
// one place: scattering string literals through the code is what produced, on the
// pipeline side, test families filed four different ways.
type Keys struct{}

// RawArtifact names a raw artifact produced by a run.
//
// `runName` is the PipelineRun's name: unique by construction, and directly
// correlatable to the run that produced it without a search or a dated convention.
func (Keys) RawArtifact(runName, artifact string) (string, error) {
	return join(prefixRaw, runName, artifact)
}

// RawMeta names a tool's execution descriptor. Kept apart from the artifacts because
// it says whether the scanner ran, which no report carries.
func (Keys) RawMeta(runName, tool string) (string, error) {
	return join(prefixRaw, runName, "meta", tool+".json")
}

// TaskLog names a step's sealed log.
func (Keys) TaskLog(runName, taskName, step string) (string, error) {
	return join(prefixLogs, runName, taskName, step+".log")
}

// TaskLogObject names a task's sealed log: its steps concatenated in execution
// order.
//
// One object per task rather than one per step, because that is what is read: "this
// task's log". A TaskRun's steps run sequentially, so their concatenation is
// chronological.
func (Keys) TaskLogObject(runName, taskName string) (string, error) {
	key, err := join(prefixLogs, runName, taskName)
	if err != nil {
		return "", err
	}
	return key + ".log", nil
}

// TaskLogChunk names a log chunk, written while the task runs.
//
// Chunks exist because Tekton logs vanish with the pods: they must be made durable
// as they come, without waiting for the task to end. One object per chunk, and not a
// multipart upload: S3 multipart parts must weigh at least 5 MiB, which rules out a
// useful flush granularity — an incident would lose up to 5 MiB of log.
//
// Two numbers are zero-padded, and that is the point of this layout: the
// lexicographic order of the keys is the chronological order of the log.
func (Keys) TaskLogChunk(runName, taskName, step string, index, sequence int) (string, error) {
	if index < 0 || sequence < 0 {
		return "", ErrUnsafeSegment{
			Segment: fmt.Sprintf("%d/%d", index, sequence),
			Reason:  "negative step rank or chunk number",
		}
	}
	prefix, err := taskLogChunkPrefix(runName, taskName, step, index)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%08d.log", prefix, sequence), nil
}

// TaskLogChunkPrefix returns the prefix of a step's chunks, to list them at restart
// or purge them after sealing.
func (Keys) TaskLogChunkPrefix(runName, taskName, step string, index int) (string, error) {
	return taskLogChunkPrefix(runName, taskName, step, index)
}

func taskLogChunkPrefix(runName, taskName, step string, index int) (string, error) {
	if index < 0 {
		return "", ErrUnsafeSegment{Segment: fmt.Sprint(index), Reason: "negative step rank"}
	}
	key, err := join(prefixLogs, runName, taskName, "parts", fmt.Sprintf("%03d-%s", index, step))
	if err != nil {
		return "", err
	}
	return key + "/", nil
}

// TaskLogPartsPrefix returns the prefix of every chunk of a task.
func (Keys) TaskLogPartsPrefix(runName, taskName string) (string, error) {
	key, err := join(prefixLogs, runName, taskName, "parts")
	if err != nil {
		return "", err
	}
	return key + "/", nil
}

// ReleaseArtifact names a deliverable attached to a release — a delivery note, a
// test report. These objects must outlive the purge of raw artifacts.
func (Keys) ReleaseArtifact(releaseTag, name string) (string, error) {
	return join(prefixArtifacts, "releases", releaseTag, name)
}

// RunPrefix returns the prefix of a run's raw artifacts, for listing or purging.
func (Keys) RunPrefix(runName string) (string, error) {
	key, err := join(prefixRaw, runName)
	if err != nil {
		return "", err
	}
	return key + "/", nil
}
