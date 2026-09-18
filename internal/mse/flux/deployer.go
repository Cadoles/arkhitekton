package flux

import (
	"context"
	"fmt"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/version"
)

/*
 * The MSE plugin's GitOps deployer.
 *
 * It acts on no cluster, and that is doctrine and not a limitation: promoting consists in
 * setting an extra tag on an already published digest, after which Flux observes it and deploys.
 * The dashboard never connects to the target cluster — production, moreover, lives in a cluster
 * it does not see.
 *
 * What this type adds to the client: it **plans and judges**, where the client merely reads
 * objects. The planning is in `promotion.go`, alongside: it is a convention of Flux.
 */

// Settings an environment must carry for this deployer.
//
// Named here, because it is what reads them.
const (
	// SettingSuffix is the SemVer metadata the ImagePolicy's range keeps.
	SettingSuffix = "promotionSuffix"
	// SettingAcceptsPrerelease says whether the range allows pre-releases. Redundant with the real
	// policy, and confronted with it by Check — it is precisely that disagreement which must
	// prevent start-up.
	SettingAcceptsPrerelease = "acceptsPrerelease"
	// SettingKustomization names the Kustomization to read in order to observe.
	SettingKustomization = "kustomization"
)

// DefaultPromotionKind is the pipeline kind that sets the extra tag.
//
// Carried here because it is the deployer that knows by which act its plan is performed. The
// dashboard uses the same pipeline outside promotion — giving a final release its own tag from
// that of its pre-release — but that is another question: one routes to an environment, the
// other establishes an identity. The two constants coincide without merging.
const DefaultPromotionKind = "retag"

// Deployer plans and observes the promotions on top of Flux.
type Deployer struct {
	client *Client
	// kind is the pipeline kind that sets the tag. Carried by the deployer rather than guessed by
	// the caller: it is what knows by which act its plan is performed.
	kind string
	// template composes the promotion tag. Configurable, `+` being excluded from the grammar of OCI
	// tags.
	template string
}

// NewDeployer builds the GitOps deployer.
//
// The client may be nil: the dashboard stays usable without access to the Flux cluster,
// observation then answers "not observable" and start-up checks nothing. Better a screen that
// says so than a binary that refuses to start over an optional dependency.
func NewDeployer(client *Client, kind, template string) *Deployer {
	if kind == "" {
		kind = DefaultPromotionKind
	}
	return &Deployer{client: client, kind: kind, template: template}
}

// Plan composes the tag to set in order to reach an environment.
//
// The refusal is the essential part: a pre-release towards an environment whose SemVer range
// excludes them would produce a valid tag, pushed with no error, which Flux would ignore.
// [PromotionTag] carries that rule and returns it as it is.
func (d *Deployer) Plan(_ context.Context, request Deployment) (Plan, error) {
	target := PromotionTarget{
		ID:                request.Target.ID,
		Suffix:            request.Target.Setting(SettingSuffix, ""),
		AcceptsPrerelease: request.Target.Setting(SettingAcceptsPrerelease, "") == "true",
	}

	plan, err := PlanPromotion(request.Version, request.Artifact, target, d.template)
	if err != nil {
		return Plan{}, err
	}

	return Plan{
		Kind: d.kind,
		Params: map[string]string{
			"image-name": request.Name,
			"old-tag":    plan.OldTag,
			"new-tag":    plan.NewTag,
		},
		Reference: plan.NewTag,
	}, nil
}

// Observe returns what Flux kept for this environment.
//
// An **intention**, never an observation: the Kustomization says what Flux applied, not what is
// running. The proof level returned is therefore `declared`, and it is PortalData's attestation
// that can raise it — their gap is what the environments screen exists to show.
func (d *Deployer) Observe(ctx context.Context, target Target) (Observation, error) {
	observation := Observation{Resolution: ResolutionUnobservable}

	if !target.Observable || d.client == nil {
		// Production, in its independent cluster, or no Flux access. Not observable and not "unknown":
		// the interface must tell the two apart, otherwise a frozen card looks like an outage.
		return observation, nil
	}

	name := target.Setting(SettingKustomization, "")
	if name == "" {
		return observation, nil
	}

	kustomizations, err := d.client.Kustomizations(ctx)
	if err != nil {
		return observation, err
	}

	for _, kustomization := range kustomizations {
		if kustomization.Name != name {
			continue
		}
		observation.State = kustomization.State()
		observation.Resolution = ResolutionDeclared
		return observation, nil
	}
	return observation, nil
}

// CheckScheme refuses a scheme to which this deployer cannot attach a marker.
//
// This deployer promotes by **writing metadata** into the tag, which an ImagePolicy's range then
// keeps. A scheme that does not know where to put that metadata makes promotion impossible —
// and the failure would be the most expensive there is: the tag would go, the registry would
// accept it, Flux would ignore it, and the promotion would look successful.
//
// # SemVer, and it alone
//
// The scheme was queried through a `Mark` method of its interface, so that a calendar scheme
// could one day fit without this check changing. This deployer exists only for MSE, whose tags
// are SemVer: the question asked of the core for a case that will not arise was removed, and the
// marker is set here, by the rule that uses it.
func (d *Deployer) CheckScheme(scheme version.Scheme) error {
	// A probe tag, never pushed: the scheme is asked whether it **can** set a marker, not what it
	// would produce.
	probe, err := scheme.Parse(probeTag)
	if err != nil {
		// The scheme refuses even a canonical tag: it cannot describe the versions this deployer routes.
		return fmt.Errorf(
			"scheme %q does not recognise a version of the form %q, "+
				"which this deployer must be able to write: %w", scheme.ID(), probeTag, err)
	}

	if _, semver := scheme.(version.SemVer); !semver {
		return fmt.Errorf(
			"scheme %q cannot attach a promotion marker, "+
				"yet this deployer selects by SemVer metadata: a promotion "+
				"would produce a tag nothing would keep", scheme.ID())
	}
	if _, err := Mark(probe.Raw, "prod"); err != nil {
		return fmt.Errorf("scheme %q cannot mark %q: %w", scheme.ID(), probe.Raw, err)
	}
	return nil
}

// Mark suffixes a SemVer tag with its marker, separated by an underscore.
//
// An underscore and not `+`: the grammar of OCI tags excludes the latter, whereas SemVer
// reserves it for build metadata.
func Mark(tag, marker string) (string, error) {
	parsed, err := version.SemVer{}.Parse(tag)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(marker) == "" {
		return parsed.Raw, nil
	}
	return parsed.Raw + "_" + marker, nil
}

// probeTag is the canonical version used to query a scheme. Never pushed: it serves only to know
// whether the scheme can answer.
const probeTag = "1.0.0"

// Check confronts the declared environments with the real ImagePolicies.
//
// With no client, nothing is checked rather than everything refused: a deployment with no Flux
// access is a valid configuration, and failing the start-up over it would prevent using the rest
// of the dashboard.
func (d *Deployer) Check(ctx context.Context, targets []Target) error {
	if d.client == nil {
		return nil
	}

	policies, err := d.client.ImagePolicies(ctx)
	if err != nil {
		return fmt.Errorf("reading the ImagePolicies: %w", err)
	}

	environments := make([]Environment, 0, len(targets))
	for _, target := range targets {
		environments = append(environments, Environment{
			ID:                target.ID,
			PromotionSuffix:   target.Setting(SettingSuffix, ""),
			AcceptsPrerelease: target.Setting(SettingAcceptsPrerelease, "") == "true",
			Observable:        target.Observable,
		})
	}
	return CheckPromotionTargets(environments, policies)
}
