package worker

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/pipelines"
)

// installingCluster holds a Pipeline only once the installer has put it there.
type installingCluster struct {
	installed bool
	err       error
	// shipped is, by definition, the plugin whose label it carries.
	shipped map[string]string
}

func (c *installingCluster) Prune(_ context.Context, carried func(plugin string) bool) ([]string, error) {
	var withdrawn []string
	for name, plugin := range c.shipped {
		if !carried(plugin) {
			delete(c.shipped, name)
			withdrawn = append(withdrawn, name)
		}
	}
	return withdrawn, nil
}

func (c *installingCluster) InstallResources(context.Context, []pipelines.Resource) (pipelines.Outcome, error) {
	c.installed = true
	return pipelines.Outcome{Installed: []string{"php-unit-tests"}}, nil
}

func (c *installingCluster) Pipelines(context.Context) (map[string]catalog.PipelineSpec, error) {
	if c.err != nil {
		return nil, c.err
	}
	if !c.installed {
		return map[string]catalog.PipelineSpec{}, nil
	}
	return map[string]catalog.PipelineSpec{"php-unit-tests": {Name: "php-unit-tests"}}, nil
}

func (c *installingCluster) Tasks(context.Context) (map[string]bool, error) {
	return map[string]bool{}, c.err
}

type recordedAvailability struct{ published []map[string]string }

func (r *recordedAvailability) SetAvailability(unavailable map[string]string) {
	r.published = append(r.published, unavailable)
}

func twoPipelines() *catalog.Catalog {
	return &catalog.Catalog{Pipelines: []catalog.Pipeline{
		{Kind: "unit-tests", PipelineRef: "php-unit-tests"},
		{Kind: "e2e-tests", PipelineRef: "e2e-test"},
	}}
}

// On a fresh cluster the dashboard installs, then checks. The other order refused to start before
// the dashboard could install anything.
func TestAFreshClusterIsCheckedAfterInstalling(t *testing.T) {
	cluster := &installingCluster{}
	sink := &recordedAvailability{}
	reconciler := NewCapabilityReconciler(cluster, func() []pipelines.Resource { return []pipelines.Resource{{Name: "php-unit-tests"}} },
		twoPipelines(), cluster, sink, slog.New(slog.DiscardHandler))

	reconciler.Settle(context.Background())

	if len(sink.published) != 1 {
		t.Fatalf("published %d time(s)", len(sink.published))
	}
	unavailable := sink.published[0]
	if _, refused := unavailable["unit-tests"]; refused {
		t.Errorf("unit-tests refused although just installed: %v", unavailable)
	}
	if !strings.Contains(unavailable["e2e-tests"], "e2e-test") {
		t.Errorf("e2e-tests = %q, expected its missing Pipeline named", unavailable["e2e-tests"])
	}
}

// A cluster briefly unreachable does not make every pipeline unavailable: the previous answer stays.
func TestAnUnreachableClusterKeepsThePreviousAnswer(t *testing.T) {
	cluster := &installingCluster{err: errors.New("connection refused")}
	sink := &recordedAvailability{}
	reconciler := NewCapabilityReconciler(nil, nil, twoPipelines(), cluster, sink, slog.New(slog.DiscardHandler))

	reconciler.Settle(context.Background())

	if len(sink.published) != 0 {
		t.Errorf("published %v on an unreachable cluster", sink.published)
	}
}

// An uninstalled plugin's definitions are withdrawn; when what is carried cannot be read, nothing is.
func TestAPassWithdrawsWhatNoPluginCarries(t *testing.T) {
	cluster := &installingCluster{shipped: map[string]string{"go-unit-tests": "gotest", "php-unit-tests": "php"}}
	carried := map[string]bool{"php": true}
	var failure error
	reconciler := NewCapabilityReconciler(cluster, func() []pipelines.Resource { return []pipelines.Resource{{Name: "php-unit-tests"}} },
		twoPipelines(), cluster, &recordedAvailability{}, slog.New(slog.DiscardHandler)).
		WithPruning(func(context.Context) (map[string]bool, error) { return carried, failure })

	failure = errors.New("database unreachable")
	reconciler.Settle(context.Background())
	if len(cluster.shipped) != 2 {
		t.Fatalf("withdrawn on a failed read: %v", cluster.shipped)
	}

	failure = nil
	reconciler.Settle(context.Background())
	if _, kept := cluster.shipped["go-unit-tests"]; kept || len(cluster.shipped) != 1 {
		t.Errorf("shipped = %v", cluster.shipped)
	}
}
