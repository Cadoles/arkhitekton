// Package reaction carries the declaration "on this fact, run this plan".
//
// It knows the shape of a reaction and how it matches a fact. It does not know what
// its steps' references point at: that a kind exists in the catalogue, that an
// action is declared in the registry, that a fact type is produced by a plugin —
// three questions whose answers live elsewhere, and which are asked when saving, in
// the layer that has those registries at hand.
//
// The separation is the same as for internal/plan: structure here, references when
// they are resolved.
//
// Matching stays poor, deliberately: equality and membership, nothing else. The day
// someone asks for "if the score is above 80 and the tag does not contain hotfix",
// the right answer is a named operator, not an interpreter.
package reaction

import (
	"errors"
	"fmt"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
)

// Reaction is the declaration of a chained run.
type Reaction struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"-"`
	Name      string `json:"name"`
	// EventType is the fact listened for: `release.created`, or
	// `gitea.pull_request.opened` for a plugin fact.
	EventType string `json:"event_type"`
	Enabled   bool   `json:"enabled"`
	// Match matches the fact's payload. Empty: the reaction leaves on any fact of that
	// type.
	//
	// A flat map, like the payload it matches: a match is on a value, not on a path.
	// `$.release.kind` would require a path language and the interface that goes with
	// it.
	Match map[string]string `json:"match,omitempty"`
	Plan  plan.Plan         `json:"plan"`
}

// ErrInvalid reports a reaction we refuse to save.
var ErrInvalid = errors.New("invalid reaction")

// maxNameLength bounds the name. It serves to find a reaction in a list, not to
// describe it.
const maxNameLength = 120

// Validate refuses a reaction that could not trigger as it reads.
//
// Checked when saved rather than at the first fact: a faulty declaration discovered
// when it should have left shows up only as an absence, and an absence is hard to
// diagnose — one first looks for whether the fact was emitted at all.
func (r Reaction) Validate() error {
	var problems []error

	if name := strings.TrimSpace(r.Name); name == "" {
		problems = append(problems, fmt.Errorf(
			"%w: a name is required — it is how the reaction is found in a list",
			ErrInvalid))
	} else if len(name) > maxNameLength {
		problems = append(problems, fmt.Errorf(
			"%w: name of %d characters, bounded to %d", ErrInvalid, len(name), maxNameLength))
	}

	if strings.TrimSpace(r.EventType) == "" {
		problems = append(problems, fmt.Errorf(
			"%w: no fact listened for", ErrInvalid))
	}

	for key, value := range r.Match {
		if strings.TrimSpace(key) == "" {
			problems = append(problems, fmt.Errorf(
				"%w: a match is on an unnamed field", ErrInvalid))
		}
		// An empty value would match a field absent from the payload, which the rule
		// refuses elsewhere. Letting it through would give a reaction that never leaves
		// with nobody knowing why.
		if strings.TrimSpace(value) == "" {
			problems = append(problems, fmt.Errorf(
				"%w: the match on %q has no value", ErrInvalid, key))
		}
	}

	if err := r.Plan.Validate(); err != nil {
		problems = append(problems, err)
	}

	return errors.Join(problems...)
}

// Matches says whether a fact's payload satisfies the match.
//
// Every declared key must match: a match is a conjunction. Two conditions where
// either would do are written as two reactions, which reads better than one more
// operator.
//
// A key absent from the payload does not match. The opposite would fire a reaction
// on a fact that does not carry what it talks about.
func (r Reaction) Matches(payload map[string]string) bool {
	for key, expected := range r.Match {
		value, present := payload[key]
		if !present || !oneOf(value, expected) {
			return false
		}
	}
	return true
}

// oneOf says whether a value appears in a comma-separated list.
//
// Membership is written this way rather than with a dedicated operator:
// `preprod,production` is typed into a text field and reads back without
// documentation, where a set syntax would have to be learnt.
func oneOf(value, expected string) bool {
	for _, candidate := range strings.Split(expected, ",") {
		if strings.TrimSpace(candidate) == value {
			return true
		}
	}
	return false
}

// Refs lists what the plan's steps point at, by kind.
//
// It serves the layer that has the registries at hand: it checks that each
// reference exists when saving, rather than leaving the discovery to the moment the
// step should have left.
func (r Reaction) Refs() (pipelines, actions []string) {
	// Delegated to the plan now that a release's automation carries one too: the
	// question "what does this plan point at?" belongs no more to a reaction than to an
	// automation.
	return plan.Refs(r.Plan)
}

// Trigger names the trigger of a run issued from this reaction.
//
// `reaction:<id>`, beside the `builtin:<name>` of the verification chain. Both
// share the same uniqueness per fact, which is what lets a declared reaction
// coexist with the built-in chain without either blocking the other.
func (r Reaction) Trigger() string {
	return fmt.Sprintf("reaction:%d", r.ID)
}
