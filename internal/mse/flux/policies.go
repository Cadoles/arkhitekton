package flux

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ImagePolicy is what the dashboard reads of a selection policy.
type ImagePolicy struct {
	Name string
	// Pattern is the `filterTags.pattern`: it is what says which tags this policy keeps, hence which
	// promotion suffix it expects.
	Pattern string
	// Range is the SemVer range. Its shape decides whether pre-releases are eligible, and it is the
	// most slippery point of the whole arrangement.
	Range string
	// LatestImage is what Flux kept, empty as long as it has scanned nothing.
	LatestImage string
}

// AcceptsPrerelease says whether the range selects pre-releases.
//
// A SemVer range with no pre-release component keeps **none**: `>=0.0.0` does not match
// `1.8.0-rc1`. It is the intended behaviour for production, and a trap everywhere else — a qualif
// `ImagePolicy` written without `-0` would silently ignore every RC promoted to it.
//
// The detection is deliberately coarse: the presence of a dash in the range. Reproducing
// Masterminds/semver's parsing would give a second implementation to keep in agreement with the
// first, for no gain — what counts is comparing this reading with what the `environments` table
// declares, and refusing to start if the two diverge.
func (p ImagePolicy) AcceptsPrerelease() bool {
	return strings.Contains(p.Range, "-")
}

// Suffix extracts the promotion suffix the policy expects.
//
// The documented pattern is `^(?P<semver>.+)_testing$`: the suffix is what follows the last
// underscore, before the end anchor. Return empty rather than guess when the pattern does not
// follow that shape — an exotic pattern must produce a visible gap at validation, not an invented
// match.
func (p ImagePolicy) Suffix() string {
	pattern := strings.TrimSuffix(p.Pattern, "$")

	index := strings.LastIndex(pattern, "_")
	if index < 0 || index == len(pattern)-1 {
		return ""
	}
	suffix := pattern[index+1:]

	// A suffix still containing regular-expression syntax is not one.
	if strings.ContainsAny(suffix, `.*+?()[]{}|\^$`) {
		return ""
	}
	return suffix
}

// ImagePolicies returns the Flux namespace's policies.
func (c *Client) ImagePolicies(ctx context.Context) ([]ImagePolicy, error) {
	items, err := c.list(ctx, c.imagePolicies, c.Namespace)
	if err != nil {
		return nil, err
	}

	policies := make([]ImagePolicy, 0, len(items))
	for _, item := range items {
		object := unstructured.Unstructured{Object: item}

		policy := ImagePolicy{Name: object.GetName()}
		policy.Pattern, _, _ = unstructured.NestedString(item, "spec", "filterTags", "pattern")
		policy.Range, _, _ = unstructured.NestedString(item, "spec", "policy", "semver", "range")
		policy.LatestImage, _, _ = unstructured.NestedString(item, "status", "latestRef", "name")
		if policy.LatestImage == "" {
			// Flux moved the information from `status.latestImage` to `status.latestRef` along the way.
			// Reading both avoids depending on the version installed, which this package does not hard-code.
			policy.LatestImage, _, _ = unstructured.NestedString(item, "status", "latestImage")
		}

		policies = append(policies, policy)
	}
	return policies, nil
}

// Kustomization is an environment's reconciliation state.
type Kustomization struct {
	Name string
	// Ready carries the condition of the same name: `True`, `False` or `Unknown`.
	Ready string
	// Reason and Message explain a failed reconciliation. Without them, the interface can only
	// display "not ready".
	Reason  string
	Message string
	// LastAppliedRevision is the Git revision actually applied. It is what says what is running, as
	// opposed to what was requested.
	LastAppliedRevision string
}

// State maps the Ready condition to the displayed state.
//
// Three states and not two: `Unknown` means "reconciliation in progress", which is neither a
// success nor a failure. Merging them would display an error during every normal deployment.
func (k Kustomization) State() string {
	switch k.Ready {
	case "True":
		return "ready"
	case "False":
		return "failed"
	default:
		return "reconciling"
	}
}

// Kustomizations returns the Flux namespace's reconciliations.
func (c *Client) Kustomizations(ctx context.Context) ([]Kustomization, error) {
	items, err := c.list(ctx, c.kustomizations, c.Namespace)
	if err != nil {
		return nil, err
	}

	kustomizations := make([]Kustomization, 0, len(items))
	for _, item := range items {
		object := unstructured.Unstructured{Object: item}

		entry := Kustomization{Name: object.GetName()}
		entry.LastAppliedRevision, _, _ = unstructured.NestedString(item, "status", "lastAppliedRevision")

		conditions, _, _ := unstructured.NestedSlice(item, "status", "conditions")
		for _, raw := range conditions {
			condition, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if conditionType, _, _ := unstructured.NestedString(condition, "type"); conditionType != "Ready" {
				continue
			}
			entry.Ready, _, _ = unstructured.NestedString(condition, "status")
			entry.Reason, _, _ = unstructured.NestedString(condition, "reason")
			entry.Message, _, _ = unstructured.NestedString(condition, "message")
			break
		}

		kustomizations = append(kustomizations, entry)
	}
	return kustomizations, nil
}

// FindPolicy finds the policy of a promotion suffix.
func FindPolicy(policies []ImagePolicy, suffix string) (ImagePolicy, bool) {
	for _, policy := range policies {
		if policy.Suffix() == suffix {
			return policy, true
		}
	}
	return ImagePolicy{}, false
}

// Describe renders a policy readable in an error message.
func (p ImagePolicy) Describe() string {
	return fmt.Sprintf("%s (motif %q, plage %q)", p.Name, p.Pattern, p.Range)
}
