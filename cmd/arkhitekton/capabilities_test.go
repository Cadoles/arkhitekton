package main

import (
	"reflect"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/builtin"
)

// legacyKinds maps the catalogue's kinds to the capabilities that now describe them.
var legacyKinds = map[string]string{
	"build":             "core/build",
	"retag":             "core/retag",
	"static-tests":      "php/static-tests",
	"unit-tests":        "php/unit-tests",
	"integration-tests": "mse/integration-tests",
	"e2e-tests":         "mse/e2e-tests",
	"security-audit":    "security/security-audit",
}

func TestTheBuiltinPluginsFormARegistry(t *testing.T) {
	registry, err := builtin.Registry()
	if err != nil {
		t.Fatal(err)
	}
	if ref, found := registry.ByPipelineRef("load-test"); !found || ref.String() != "k6/load" {
		t.Errorf("load-test read by %v", ref)
	}
}

// A capability that ships manifests ships the Pipeline it names.
func TestAShippedPipelineIsAmongItsManifests(t *testing.T) {
	registry, _ := builtin.Registry()
	for _, ref := range registry.Refs() {
		pipeline, _, _ := registry.Pipeline(ref)
		if len(pipeline.Spec.Manifests) == 0 {
			continue
		}
		var found bool
		for _, manifest := range pipeline.Spec.Manifests {
			if manifest.Kind == "Pipeline" && manifest.Name == pipeline.Spec.PipelineRef {
				found = true
			}
		}
		if !found {
			t.Errorf("%s ships no Pipeline %q", ref, pipeline.Spec.PipelineRef)
		}
	}
}

// Every pipeline of today's catalogue is described by a capability, field for field. Until the
// catalogue binds capabilities, the two descriptions coexist, and this is what keeps them equal.
func TestEachCatalogueKindMatchesItsCapability(t *testing.T) {
	declared, err := catalog.Load("testdata/catalog-pipelines.yaml")
	if err != nil {
		t.Fatal(err)
	}
	registry, _ := builtin.Registry()

	for _, entry := range declared.Pipelines {
		name, known := legacyKinds[entry.Kind]
		if !known {
			t.Errorf("kind %q has no capability", entry.Kind)
			continue
		}
		ref, _ := capability.ParseRef(name)
		pipeline, _, found := registry.Pipeline(ref)
		if !found {
			t.Errorf("%s: not registered", ref)
			continue
		}
		compareCapability(t, entry, pipeline.Spec)
	}
}

func compareCapability(t *testing.T, entry catalog.Pipeline, spec capability.PipelineSpec) {
	t.Helper()
	where := entry.Kind

	if entry.PipelineRef != spec.PipelineRef {
		t.Errorf("%s: pipelineRef %q, capability %q", where, entry.PipelineRef, spec.PipelineRef)
	}
	if entry.Verification != spec.Verification {
		t.Errorf("%s: verification %v, capability %v", where, entry.Verification, spec.Verification)
	}
	if (entry.MaxConcurrent == 1) != spec.Exclusive {
		t.Errorf("%s: maxConcurrent %d, capability exclusive %v", where, entry.MaxConcurrent, spec.Exclusive)
	}

	var products []string
	for _, product := range spec.Produces {
		products = append(products, string(product))
	}
	var declaredProducts []string
	for _, product := range entry.Produces {
		declaredProducts = append(declaredProducts, string(product))
	}
	if strings.Join(products, ",") != strings.Join(declaredProducts, ",") {
		t.Errorf("%s: produces %v, capability %v", where, declaredProducts, products)
	}

	if len(entry.Params) != len(spec.Params) {
		t.Errorf("%s: %d params, capability %d", where, len(entry.Params), len(spec.Params))
	}
	for name, param := range entry.Params {
		declared, found := spec.Params[name]
		if !found {
			t.Errorf("%s: param %q missing from the capability", where, name)
			continue
		}
		want := capability.Param{
			Required: param.Required, Suggest: param.Suggest, Image: param.Image,
			Label: strings.TrimSpace(param.Label), Help: strings.TrimSpace(param.Help),
			Placeholder: param.Placeholder,
		}
		switch param.Source {
		case catalog.SourceConfig:
			want.Source, want.DefaultKey = capability.ParamBinding, param.Key
		case catalog.SourceLiteral:
			want.Source, want.Value = capability.ParamLiteral, param.Value
		default:
			want.Source = capability.ParamInput
		}
		if declared != want {
			t.Errorf("%s: param %q\n  catalogue  %+v\n  capability %+v", where, name, want, declared)
		}
	}

	artifacts := map[string]capability.Artifact{}
	for _, artifact := range spec.Artifacts {
		artifacts[artifact.Name] = artifact
	}
	if len(entry.Uploads) != len(spec.Artifacts) {
		t.Errorf("%s: %d uploads, capability %d artifacts", where, len(entry.Uploads), len(spec.Artifacts))
	}
	for name, upload := range entry.Uploads {
		artifact, found := artifacts[name]
		if !found || artifact.Param != upload.Param || artifact.Object != upload.Artifact ||
			string(artifact.Format) != upload.Format {
			t.Errorf("%s: upload %q = %+v, capability %+v", where, name, upload, artifact)
		}
	}

	workspaces := map[string]capability.Workspace{}
	for _, workspace := range spec.Workspaces {
		workspaces[workspace.Name] = workspace
	}
	if len(entry.Workspaces) != len(spec.Workspaces) {
		t.Errorf("%s: %d workspaces, capability %d", where, len(entry.Workspaces), len(spec.Workspaces))
	}
	for _, workspace := range entry.Workspaces {
		declared, found := workspaces[workspace.Name]
		switch {
		case !found:
			t.Errorf("%s: workspace %q missing from the capability", where, workspace.Name)
		case workspace.Secret != nil && declared.Need == "":
			t.Errorf("%s: workspace %q is a secret, the capability declares no need", where, workspace.Name)
		case workspace.VolumeClaimTemplate != nil &&
			(declared.Volume == nil || declared.Volume.Storage != workspace.VolumeClaimTemplate.Storage):
			t.Errorf("%s: workspace %q volume %+v, capability %+v",
				where, workspace.Name, workspace.VolumeClaimTemplate, declared.Volume)
		}
	}

	var deploys bool
	for _, need := range spec.Needs {
		if need.Name == capability.NeedClusterDeploy {
			deploys = true
		}
	}
	if (entry.ServiceAccountName != "") != deploys {
		t.Errorf("%s: service account %q, capability deploys %v", where, entry.ServiceAccountName, deploys)
	}
}

// The shipped catalogue, now made of bindings, resolves to exactly the pipelines it used to
// declare whole. Every field the scheduler reads to build a PipelineRun is compared.
func TestTheBoundCatalogueResolvesToTheDeclaredPipelines(t *testing.T) {
	legacy, err := catalog.Load("testdata/catalog-pipelines.yaml")
	if err != nil {
		t.Fatal(err)
	}
	bound := shippedCatalogue(t)

	if len(bound.Pipelines) != len(legacy.Pipelines) {
		t.Fatalf("%d pipelines, %d before", len(bound.Pipelines), len(legacy.Pipelines))
	}
	for index, before := range legacy.Pipelines {
		after := bound.Pipelines[index]
		if after.Unavailable != "" {
			t.Errorf("%s: unavailable (%s)", after.Kind, after.Unavailable)
		}
		after.Capability, after.Unavailable, after.ExternalTasks = "", "", nil
		if !reflect.DeepEqual(before, after) {
			t.Errorf("%s resolves differently:\n  before %+v\n  after  %+v", before.Kind, before, after)
		}
	}
}

func shippedCatalogue(t *testing.T) *catalog.Catalog {
	t.Helper()
	registry, err := builtin.Registry()
	if err != nil {
		t.Fatal(err)
	}
	shipped, err := catalog.Load("../../deploy/base/config/catalog.yaml", catalog.WithCapabilities(registry))
	if err != nil {
		t.Fatalf("shipped catalogue: %v", err)
	}
	return shipped
}
