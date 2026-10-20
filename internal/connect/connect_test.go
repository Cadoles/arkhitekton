package connect

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

// settingsStore resolves plugins per project, sealed values prefixed with `sealed:`.
type settingsStore struct {
	global  map[string]store.PluginSettings
	project map[string]map[string]string
	off     map[string]bool
}

func (s settingsStore) ResolvedPlugin(_ context.Context, _ int64, pluginID string) (store.PluginSettings, error) {
	if s.off[pluginID] {
		return store.PluginSettings{}, store.ErrPluginDisabled
	}
	return store.PluginSettings{PluginID: pluginID, Enabled: true, Config: s.project[pluginID]}, nil
}

func (s settingsStore) PluginSettingsAll(context.Context) (map[string]store.PluginSettings, error) {
	return s.global, nil
}

func (s settingsStore) GetProject(_ context.Context, id int64) (store.Project, error) {
	return store.Project{ID: id, Slug: "mse", Repository: "CNOUS/mse"}, nil
}

type opener struct{}

func (opener) Open(sealed string) (string, error) {
	if !strings.HasPrefix(sealed, "sealed:") {
		return "", errors.New("not sealed")
	}
	return strings.TrimPrefix(sealed, "sealed:"), nil
}
func (opener) Configured() bool { return true }

// recordingForge records the target it is connected with.
type recordingForge struct {
	capability.ForgeCapability
	targets *[]capability.Target
}

func (f recordingForge) Connect(target capability.Target) (provider.Forge, error) {
	*f.targets = append(*f.targets, target)
	return nil, nil
}

func (f recordingForge) Prober(target capability.Target) (provider.RepositoryProber, error) {
	*f.targets = append(*f.targets, target)
	return prober{}, nil
}

type prober struct{}

func (prober) Probe(context.Context, string, string) (provider.Probe, error) {
	return provider.Probe{}, nil
}

type linkTracker struct{ capability.TrackerCapability }

func registry(t *testing.T, targets *[]capability.Target) *capabilities.Registry {
	t.Helper()
	field := func(name string, secret bool) capability.Field {
		return capability.Field{Name: name, Type: capability.FieldText, Secret: secret}
	}
	built, err := capabilities.New(
		capability.Plugin{
			ID: "gitea", Version: "1", APIVersion: sdk.APIVersion, Forge: recordingForge{targets: targets},
			Global: []capability.Field{field("url", false), field("token", true)},
		},
		capability.Plugin{
			ID: "redmine", Version: "1", APIVersion: sdk.APIVersion, Tracker: linkTracker{},
			Project: []capability.Field{field("review_statuses", false)},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return built
}

// A project's forge is connected with its plugin's settings, unsealed, and its own repository.
func TestAForgeIsConnectedWithTheProjectsSettings(t *testing.T) {
	var targets []capability.Target
	connections := New(registry(t, &targets), settingsStore{project: map[string]map[string]string{
		"gitea": {"url": "https://forge.example.fr", "token": "sealed:secret"},
	}}, opener{}, Timeouts{Forge: 7})

	if _, err := connections.Forge(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %+v", targets)
	}
	target := targets[0]
	if target.Repository != "CNOUS/mse" || target.Settings["token"] != "secret" ||
		target.Settings["url"] != "https://forge.example.fr" || target.Timeout != 7 {
		t.Errorf("target = %+v", target)
	}
}

// With its forge plugin switched off, a project has no forge: said as such, so the screens fall
// back to what works without it.
func TestASwitchedOffForgeIsNotConfigured(t *testing.T) {
	var targets []capability.Target
	connections := New(registry(t, &targets), settingsStore{off: map[string]bool{"gitea": true}}, opener{}, Timeouts{})

	if _, err := connections.Forge(context.Background(), 1); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("err = %v", err)
	}
	if len(targets) != 0 {
		t.Errorf("connected anyway: %+v", targets)
	}
}

// The review statuses are the project's tracker setting, split as every list setting is.
func TestTheReviewStatusesAreTheProjectsOwn(t *testing.T) {
	var targets []capability.Target
	connections := New(registry(t, &targets), settingsStore{project: map[string]map[string]string{
		"redmine": {"review_statuses": " En revue , Validé "},
	}}, opener{}, Timeouts{})

	statuses, err := connections.ReviewStatuses(context.Background(), 1)
	if err != nil || !slices.Equal(statuses, []string{"En revue", "Validé"}) {
		t.Errorf("statuses %q, err %v", statuses, err)
	}
}

// A repository being declared is checked with the instance's forge settings: its project may not
// exist yet.
func TestARepositoryIsProbedWithTheInstancesForge(t *testing.T) {
	var targets []capability.Target
	connections := New(registry(t, &targets), settingsStore{global: map[string]store.PluginSettings{
		"gitea": {PluginID: "gitea", Enabled: true, Config: map[string]string{"url": "https://f", "token": "sealed:t"}},
	}}, opener{}, Timeouts{})

	if _, err := connections.Probe(context.Background(), "CNOUS/new", ""); err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Settings["token"] != "t" {
		t.Errorf("targets = %+v", targets)
	}
}
