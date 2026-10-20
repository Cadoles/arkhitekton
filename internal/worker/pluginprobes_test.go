package worker

import (
	"context"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// observingProbe returns the observations it is given.
type observingProbe struct {
	observed []capability.Observation
	target   capability.Target
}

func (p *observingProbe) Name() string                  { return "pull_requests" }
func (p *observingProbe) Schedule() capability.Schedule { return capability.Schedule{} }
func (p *observingProbe) Poll(
	_ context.Context, target capability.Target, _ string,
) ([]capability.Observation, string, error) {
	p.target = target
	return p.observed, "next", nil
}

func observingRegistry(t *testing.T, probe *observingProbe) *capabilities.Registry {
	t.Helper()
	registry, err := capabilities.New(capability.Plugin{
		ID: "gitea", Version: "1.0.0", APIVersion: sdk.APIVersion,
		Events: []capability.EventSpec{{Suffix: "pull_request.opened"}},
		Probes: []capability.Probe{probe},
	})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

// The dashboard names a plugin's facts: the type carries the plugin's prefix, the key the type, and
// the cursor the plugin and the probe.
func TestAPluginsObservationIsNamedByTheDashboard(t *testing.T) {
	opened := time.Date(2026, 9, 3, 10, 30, 0, 0, time.UTC)
	probe := &observingProbe{observed: []capability.Observation{{
		Suffix: "pull_request.opened", Subject: "#412", Key: "CNOUS/mse:412", OccurredAt: opened,
		Payload: map[string]string{"number": "412"},
	}}}
	probes := PluginProbes(observingRegistry(t, probe), time.Second)
	if len(probes) != 1 || probes[0].Source() != "gitea.pull_requests" || probes[0].PluginID() != "gitea" {
		t.Fatalf("probes = %+v", probes)
	}

	facts, cursor, err := probes[0].Poll(context.Background(),
		store.Project{ID: 1, Slug: "mse", Repository: "CNOUS/mse"}, map[string]string{"url": "u"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cursor != "next" || len(facts) != 1 {
		t.Fatalf("cursor %q, facts %+v", cursor, facts)
	}
	fact := facts[0]
	if fact.Type != "gitea.pull_request.opened" || fact.DedupKey != "gitea.pull_request.opened:CNOUS/mse:412" ||
		!fact.OccurredAt.Equal(opened) || fact.Subject != "#412" {
		t.Errorf("fact = %+v", fact)
	}
	if probe.target.Repository != "CNOUS/mse" || probe.target.Settings["url"] != "u" || probe.target.Timeout != time.Second {
		t.Errorf("target = %+v", probe.target)
	}
}

// A plugin cannot produce a fact its declaration does not announce: the reaction editor would not
// offer it, and nothing could listen for it.
func TestAnUndeclaredObservationIsRefused(t *testing.T) {
	probe := &observingProbe{observed: []capability.Observation{{Suffix: "release.deleted", Key: "x"}}}
	probes := PluginProbes(observingRegistry(t, probe), time.Second)

	_, _, err := probes[0].Poll(context.Background(), store.Project{ID: 1, Slug: "mse"}, nil, "")
	if err == nil || !strings.Contains(err.Error(), "release.deleted") {
		t.Errorf("%v", err)
	}
}
