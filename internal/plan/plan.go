// Package plan carries the execution graph of a reaction, and its progress.
//
// No I/O, deliberately. Nothing here touches the database or the cluster. How a
// plan advances — which stage is settled, which step leaves next, what to do with
// a failure — is decision logic, and it is testable without standing anything up.
//
// That is not a style preference. A chaining engine is rarely seen running end to
// end, and on runs that last tens of minutes; the only way to trust its awkward
// cases — an empty stage, a step that cannot leave, a failure in the middle — is
// to be able to write them in three lines.
//
// A plan is a sequence of stages; each stage carries steps run in parallel; the
// next stage starts when the previous one is settled. A free-form graph was
// rejected: it calls for an expression language, a graph scheduler, cycle
// detection and an interface nobody can read, for expressiveness the need does not
// ask for. It also has a second virtue: stacked stages and steps in columns can be
// drawn, where a DAG would have needed a graph editor.
package plan

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// GesteKind says what a step carries out.
//
// A closed set: these are the two things the engine can do. A pipeline step is a
// job of the existing queue, with its ceilings, its exclusion classes and its
// attempt limit — there is no second executor, and that is what holds the edifice
// together.
type GesteKind string

const (
	// GestePipeline queues a kind from the catalogue.
	GestePipeline GesteKind = "pipeline"
	// GesteAction runs an internal action or one supplied by a plugin.
	GesteAction GesteKind = "action"
)

// When bounds a step's triggering on the outcome of the previous stage.
//
// Three values, not an expression language. The day someone asks for "if the score
// is above 80", the right answer is a named operator, not an interpreter: a
// configuration that becomes a program is one nobody can read back, and the
// dashboard offers no debugger for it.
type When string

const (
	// WhenSuccess leaves only if the previous stage failed nothing. The default: it is
	// the chaining one writes without thinking.
	WhenSuccess When = "success"
	// WhenFailure leaves only if the previous stage failed. For reacting to a failure
	// — setting a red status, warning someone.
	WhenFailure When = "failure"
	// WhenAlways leaves either way. For concluding: a final status, a clean-up.
	WhenAlways When = "always"
)

// Geste is one thing to do.
type Geste struct {
	Kind GesteKind `json:"kind"`
	// Ref names the catalogue kind for a pipeline, the action for an action. What the
	// reference points at is checked when saved, against the catalogue and the
	// registry — this package knows neither.
	Ref string `json:"ref"`
	// Label names the step on screen. Empty, the reference will do.
	Label string `json:"label,omitempty"`
	// An empty When means [WhenSuccess].
	When When `json:"when,omitempty"`
	// Params is what the declaration fixes. What depends on the triggering fact — the
	// revision built, the tag — is bound by the engine as the step leaves: a plan
	// freezing those values would serve only once.
	Params map[string]string `json:"params,omitempty"`
	// Weight is this step's weight in a release's quality score.
	//
	// On the step, and no longer on the signal: the score used to weigh six signals of
	// fixed weight, so two distinct end-to-end campaigns could not each carry their
	// own. The signal still says what a report reader should extract, but it is no
	// longer the axis of weighting.
	//
	// A pointer, for three states rather than two. Absent: the step takes its signal's
	// original weight, so nobody loses the agreed weighting by typing nothing. Zero:
	// the step is deliberately out of the score, which is a decision and not an
	// oversight. A value: it applies.
	//
	// `plan_runs.plan` carries the plan as it stood when the run opened, so the weight
	// is frozen with the rest: replaying the score of a release shipped six months ago
	// uses that day's weights. A figure once communicated does not rewrite itself.
	Weight *float64 `json:"weight,omitempty"`
}

// Condition returns the effective rule; absence means [WhenSuccess].
func (g Geste) Condition() When {
	if g.When == "" {
		return WhenSuccess
	}
	return g.When
}

// Name returns something to call the step by on screen.
func (g Geste) Name() string {
	if strings.TrimSpace(g.Label) != "" {
		return g.Label
	}
	return g.Ref
}

// Stage is a set of steps run in parallel. Role says what a stage is, for whatever
// depends on it outside.
//
// A release's life advances at two moments: when it is built, and when it is
// verified. There must therefore be a named anchor in the plan.
//
// "The plan concluded" is not one: it confuses the automation being over with the
// release being verified. A plan ending in a notification would then fail the
// release on an unreachable webhook.
//
// A composite step — a `build` step containing pipelines — is not one either: the
// stage already is the group. It gathers parallel steps and the engine already
// computes its outcome; one more level of nesting would redo what it does.
//
// A stage with no role touches nothing, which is the default and what makes a
// notification stage harmless to a release's life cycle.
type Role string

const (
	// RoleBuild produces a release's artifacts.
	RoleBuild Role = "build"
	// RoleTest yields a verdict on those artifacts.
	RoleTest Role = "test"
)

// Roles lists the known roles, for the editor and for validation.
func Roles() []Role { return []Role{RoleBuild, RoleTest} }

type Stage struct {
	// Label names the stage on screen. Optional.
	Label string `json:"label,omitempty"`
	// Role attaches the stage to a release's life cycle. Empty: the stage runs without
	// changing anything in it.
	//
	// This package checks that the value is known. Whether the stage's steps
	Role   Role    `json:"role,omitempty"`
	Gestes []Geste `json:"gestes"`
}

// Plan is a sequence of stages.
type Plan struct {
	Stages []Stage `json:"stages"`
}

// Empty says a plan has nothing to do.
//
// Distinct from an invalid plan: "this project chains nothing" is a perfectly
// legitimate configuration, and the caller must be able to conclude at once rather
// than wait for a step that will never come.
func (p Plan) Empty() bool {
	for _, stage := range p.Stages {
		if len(stage.Gestes) > 0 {
			return false
		}
	}
	return true
}

// ErrInvalid reports a plan we refuse to run.
var ErrInvalid = errors.New("invalid plan")

// Validate refuses a plan that could not run as it reads.
//
// Checked when saved rather than at the first trigger: a faulty plan discovered
// when it should have left leaves a release waiting for a step nothing will run,
// and the diagnosis then happens on a halted chain rather than in front of a form.
func (p Plan) Validate() error {
	if len(p.Stages) == 0 {
		return fmt.Errorf("%w: no stage", ErrInvalid)
	}

	var problems []error
	for index, stage := range p.Stages {
		if len(stage.Gestes) == 0 {
			problems = append(problems, fmt.Errorf(
				"%w: stage %d carries no step", ErrInvalid, index+1))
			continue
		}
		problems = append(problems, stage.validateRole(index)...)
		problems = append(problems, stage.validateWeights(index)...)
		for rank, geste := range stage.Gestes {
			problems = append(problems, geste.validate(index, rank)...)
		}
	}
	return errors.Join(problems...)
}

// validateRole refuses a role this package does not know.
func (s Stage) validateRole(index int) []error {
	if s.Role == "" || slices.Contains(Roles(), s.Role) {
		return nil
	}

	known := make([]string, 0, len(Roles()))
	for _, role := range Roles() {
		known = append(known, string(role))
	}
	// An invented role would be ignored by the life cycle, and the stage would run
	// without ever advancing the release. Refusing it when saved avoids a release that
	// stays building with nothing saying so.
	return []error{fmt.Errorf("%w: stage %d carries role %q (known: %s)",
		ErrInvalid, index+1, s.Role, strings.Join(known, ", "))}
}

// ReleaseTrigger names the trigger of a release's plan.
//
// Here rather than in the worker, now that two packages compose it: the one that
// opens the run, and the one that finds it again to read a score's frozen weights.
// Two spellings would name two runs, and the score would be computed on a plan
// that never ran.
const ReleaseTrigger = "builtin:release"

// Refs returns what a plan points at, by kind of step.
//
// A package function rather than a method on a reaction: now that a release's
// automation carries a plan too, the question "what does this plan point at?"
// arises on both sides, and two answers would diverge at the first kind added.
func Refs(p Plan) (pipelines, actions []string) {
	for _, stage := range p.Stages {
		for _, geste := range stage.Gestes {
			switch geste.Kind {
			case GestePipeline:
				pipelines = append(pipelines, geste.Ref)
			case GesteAction:
				actions = append(actions, geste.Ref)
			}
		}
	}
	return pipelines, actions
}

// validateWeights refuses a weight where nothing would read it.
//
// A release's score is computed on the steps of its verification stage: the only
// one that measures anything. A weight set elsewhere — on a build, on a
// notification — would be saved, displayed, and have no effect.
func (s Stage) validateWeights(index int) []error {
	if s.Role == RoleTest {
		return nil
	}

	var problems []error
	for rank, geste := range s.Gestes {
		if geste.Weight == nil {
			continue
		}
		problems = append(problems, fmt.Errorf(
			"%w: stage %d, step %d: a weight only counts in the score of a stage with "+
				"role %q (this one carries %q)",
			ErrInvalid, index+1, rank+1, RoleTest, s.Role))
	}
	return problems
}

// LastWithRole returns the index of the last stage carrying this role, or -1.
//
// The last, not the first: a plan may build in two passes — a base image, then the
// application on top — and it is the conclusion of the last of those that says the
// build is done.
func (p Plan) LastWithRole(role Role) int {
	found := -1
	for index, stage := range p.Stages {
		if stage.Role == role {
			found = index
		}
	}
	return found
}

func (g Geste) validate(stage, rank int) []error {
	var problems []error
	where := fmt.Sprintf("stage %d, step %d", stage+1, rank+1)

	switch g.Kind {
	case GestePipeline, GesteAction:
	default:
		problems = append(problems, fmt.Errorf(
			"%w: %s: unknown kind %q (expected %s or %s)",
			ErrInvalid, where, g.Kind, GestePipeline, GesteAction))
	}

	if strings.TrimSpace(g.Ref) == "" {
		problems = append(problems, fmt.Errorf(
			"%w: %s: missing reference — a step must say what it carries out",
			ErrInvalid, where))
	}

	switch g.Condition() {
	case WhenSuccess, WhenFailure, WhenAlways:
	default:
		problems = append(problems, fmt.Errorf(
			"%w: %s: unknown condition %q (expected %s, %s or %s)",
			ErrInvalid, where, g.When, WhenSuccess, WhenFailure, WhenAlways))
	}

	// A step in the first stage conditioned on failure would never leave: nothing
	// precedes it, and the conventional outcome of an absent stage is success. Refused
	// rather than left in place, because a declaration that cannot trigger reads like
	// one that does not trigger — and the fault is then looked for elsewhere.
	if stage == 0 && g.Condition() == WhenFailure {
		problems = append(problems, fmt.Errorf(
			"%w: %s: condition %q on the first stage — nothing precedes it, "+
				"this step would never leave", ErrInvalid, where, WhenFailure))
	}

	// A negative weight would lower the score when the step succeeds and raise it when
	// it fails. Renormalisation would also make it incomputable as soon as the sum of
	// measured weights reached zero.
	if g.Weight != nil && *g.Weight < 0 {
		problems = append(problems, fmt.Errorf(
			"%w: %s: negative weight (%v) — a step cannot subtract from the score",
			ErrInvalid, where, *g.Weight))
	}

	return problems
}

/*
 * The progress.
 *
 * A pure function from a plan and the state of its steps to what remains to be done. The engine
 * persists the states and runs them; it decides nothing.
 */

// State is a step's fate.
type State string

const (
	Pending   State = "pending"
	Running   State = "running"
	Succeeded State = "succeeded"
	Failed    State = "failed"
	// Skipped: the step's condition was not met. Distinct from a failure — a skipped
	// step missed nothing — and from pending, which would still be waiting.
	Skipped State = "skipped"
)

// Terminal says a step will not move again.
func (s State) Terminal() bool {
	return s == Succeeded || s == Failed || s == Skipped
}

// Position points at a step within a plan.
type Position struct {
	Stage int `json:"stage"`
	Geste int `json:"geste"`
}

// Progress is a plan and the state of its steps.
type Progress struct {
	Plan Plan
	// States follows the shape of the plan: one slice per stage, one state per step.
	States [][]State
}

// Start returns a plan's initial progress: everything pending.
func Start(p Plan) Progress {
	states := make([][]State, len(p.Stages))
	for index, stage := range p.Stages {
		states[index] = make([]State, len(stage.Gestes))
		for rank := range stage.Gestes {
			states[index][rank] = Pending
		}
	}
	return Progress{Plan: p, States: states}
}

// Step is what the engine must do with a pending step.
type Step struct {
	Position Position
	Geste    Geste
	// Run says whether it must leave. False: its condition is not met and it is to be
	// marked [Skipped].
	//
	// One answer for both cases rather than two functions to call in the right order:
	// an engine forgetting the second would leave steps pending for ever, and the
	// stage would never settle.
	Run bool
}

// Next returns the steps of the current stage awaiting a decision.
//
// It returns an empty slice when the plan is settled, or when the current stage
// only holds steps that already left. Callable again with no effect: that is what
// makes the engine tolerant of a redelivery, and what allows restarting it without
// keeping state in memory.
func (p Progress) Next() []Step {
	stage, found := p.current()
	if !found {
		return nil
	}

	// The previous stage's outcome decides the conditions. The first stage has none:
	// by convention, nothing failed before it.
	previousFailed := stage > 0 && p.stageFailed(stage-1)

	var steps []Step
	for rank, geste := range p.Plan.Stages[stage].Gestes {
		if p.States[stage][rank] != Pending {
			continue
		}
		steps = append(steps, Step{
			Position: Position{Stage: stage, Geste: rank},
			Geste:    geste,
			Run:      satisfied(geste.Condition(), previousFailed),
		})
	}
	return steps
}

// satisfied says whether a step's condition is met.
func satisfied(condition When, previousFailed bool) bool {
	switch condition {
	case WhenAlways:
		return true
	case WhenFailure:
		return previousFailed
	default:
		return !previousFailed
	}
}

// current returns the index of the first unsettled stage.
func (p Progress) current() (int, bool) {
	for index := range p.Plan.Stages {
		if !p.stageSettled(index) {
			return index, true
		}
	}
	return 0, false
}

// stageSettled says a stage will not move again.
func (p Progress) stageSettled(stage int) bool {
	for _, state := range p.States[stage] {
		if !state.Terminal() {
			return false
		}
	}
	return true
}

// stageFailed says a stage left something failed.
//
// A wholly skipped stage has not failed: it attempted nothing. The distinction
// matters for a step conditioned on failure, which must not leave because the
// preceding stage did not run.
func (p Progress) stageFailed(stage int) bool {
	return slices.Contains(p.States[stage], Failed)
}

// StageOutcome returns the outcome of a settled stage.
//
// The second return is false while it is still moving. That is what lets the engine
// tell a release's life cycle at the moment the build concludes, without waiting
// for the end of the plan — a plan may carry a notification stage afterwards, which
// has nothing to do with the release's state.
//
// A wholly skipped stage is settled and has not failed: it attempted nothing. A
// skipped build must not fail a release, it must leave it where it is.
func (p Progress) StageOutcome(stage int) (State, bool) {
	if stage < 0 || stage >= len(p.States) || !p.stageSettled(stage) {
		return Pending, false
	}
	if p.stageFailed(stage) {
		return Failed, true
	}
	if slices.Contains(p.States[stage], Succeeded) {
		return Succeeded, true
	}
	// Neither failure nor success: everything was skipped.
	return Skipped, true
}

// Done says the plan has nothing left to do.
func (p Progress) Done() bool {
	_, found := p.current()
	return !found
}

// Outcome is a settled plan's outcome.
//
// Failed as soon as one step failed, wherever it is. A concluding `always` step
// that succeeds does not redeem the failure that made it leave: the plan would
// report success on a chain whose build fell over.
func (p Progress) Outcome() State {
	for stage := range p.Plan.Stages {
		if p.stageFailed(stage) {
			return Failed
		}
	}
	return Succeeded
}

// Mark records a step's fate.
//
// It returns a copy: Progress is a value, and mutating it in place would make a
// caller that passed it to Next see its slice change underneath.
func (p Progress) Mark(position Position, state State) (Progress, error) {
	if position.Stage < 0 || position.Stage >= len(p.States) {
		return p, fmt.Errorf("%w: stage %d outside the plan", ErrInvalid, position.Stage)
	}
	if position.Geste < 0 || position.Geste >= len(p.States[position.Stage]) {
		return p, fmt.Errorf("%w: step %d outside stage %d",
			ErrInvalid, position.Geste, position.Stage)
	}

	states := make([][]State, len(p.States))
	for index, stage := range p.States {
		states[index] = slices.Clone(stage)
	}
	states[position.Stage][position.Geste] = state
	return Progress{Plan: p.Plan, States: states}, nil
}
