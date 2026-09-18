package flux

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Environment is what the guard rail needs to know about an environment.
//
// Its own type rather than `store`'s, so that this package does not depend on the database — and
// above all so that the check is testable without PostgreSQL.
type Environment struct {
	ID                string
	PromotionSuffix   string
	AcceptsPrerelease bool
	Observable        bool
}

// CheckPromotionTargets confronts the environments table with the ImagePolicies.
//
// It is the central guard rail, and it protects against a particularly vicious failure mode:
// promoting to an environment whose suffix matches no policy produces a **valid** tag, pushed
// **with no error**, which Flux ignores. The promotion looks successful and nothing is deployed.
//
// Two gaps are therefore looked for:
//
//  1. **Orphan suffix** — no `ImagePolicy` keeps this suffix. The promotion would go into the
//     void.
//  2. **Disagreement about pre-releases** — the table says it accepts RCs although the policy's
//     SemVer range excludes them, or the reverse. The first case lets the dashboard offer a
//     promotion Flux will ignore; the second makes it refuse a promotion that would have worked.
//
// The unobservable environments — production, in its independent cluster — are **excluded from
// the check**: their `ImagePolicy` objects live in a cluster the dashboard does not see, and
// requiring them here would fail the start-up over a normal absence.
//  3. **Shared suffix** — two environments promoting to the same place. One of the two would
//     never be deployed, with no error saying so. This rule lived in the catalogue's validation;
//     there it imposed a Flux concept on every catalogue, including those of a project with no
//     deployer. It belongs here.
func CheckPromotionTargets(environments []Environment, policies []ImagePolicy) error {
	var problems []error

	suffixes := make(map[string]string, len(environments))
	for _, environment := range environments {
		// Checked on **every** environment, observable or not: two environments of the same name tread
		// on each other independently of our ability to read their policies.
		if environment.PromotionSuffix == "" {
			problems = append(problems, fmt.Errorf(
				"environment %q: no promotion suffix declared — this deployer "+
					"selects by SemVer metadata and would not know what to push",
				environment.ID))
			continue
		}
		if other, clash := suffixes[environment.PromotionSuffix]; clash {
			problems = append(problems, fmt.Errorf(
				"environments %q and %q share the suffix %q — one of the two would "+
					"never be deployed",
				other, environment.ID, environment.PromotionSuffix))
		}
		suffixes[environment.PromotionSuffix] = environment.ID
	}

	for _, environment := range environments {
		if !environment.Observable {
			continue
		}

		policy, found := FindPolicy(policies, environment.PromotionSuffix)
		if !found {
			problems = append(problems, fmt.Errorf(
				"environment %q: no ImagePolicy keeps the suffix %q — a promotion "+
					"to this environment would produce a tag Flux would ignore (policies read: %s)",
				environment.ID, environment.PromotionSuffix, strings.Join(describe(policies), ", ")))
			continue
		}

		if policy.AcceptsPrerelease() != environment.AcceptsPrerelease {
			problems = append(problems, fmt.Errorf(
				"environment %q: the table declares accepts_prerelease=%v, policy %s says %v",
				environment.ID, environment.AcceptsPrerelease,
				policy.Describe(), policy.AcceptsPrerelease()))
		}
	}

	return errors.Join(problems...)
}

func describe(policies []ImagePolicy) []string {
	described := make([]string, 0, len(policies))
	for _, policy := range policies {
		suffix := policy.Suffix()
		if suffix == "" {
			suffix = "(suffixe non reconnu)"
		}
		described = append(described, fmt.Sprintf("%s→%s", policy.Name, suffix))
	}
	sort.Strings(described)
	return described
}
