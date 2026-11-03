package configure

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/configfile"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

const pinned = "registry.example/mse@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// workerFake stands for the database and the worker: a pending installation concludes the next
// time it is read, as the worker would have pulled it meanwhile.
type workerFake struct {
	installations map[int64]*store.PluginInstallation
	next          int64
	// conclude is the state a pending installation reaches when the worker has pulled it.
	conclude []string
	accepted []string
	actions  []string
	sources  map[string]store.PluginSource
}

func (f *workerFake) PluginSources(context.Context) ([]store.PluginSource, error) {
	var all []store.PluginSource
	for _, source := range f.sources {
		all = append(all, source)
	}
	return all, nil
}

func (f *workerFake) PluginSource(_ context.Context, pluginID string) (store.PluginSource, error) {
	source, found := f.sources[pluginID]
	if !found {
		return store.PluginSource{}, store.ErrSourceNotFound
	}
	return source, nil
}

func (f *workerFake) SetSourcePinned(_ context.Context, pluginID string, pinned bool) error {
	source := f.sources[pluginID]
	source.Pinned = pinned
	f.sources[pluginID] = source
	f.actions = append(f.actions, fmt.Sprintf("pin %v", pinned))
	return nil
}

func (f *workerFake) RequestUpdateCheck(context.Context, string) error { return nil }

func (f *workerFake) PluginInstallations(context.Context) ([]store.PluginInstallation, error) {
	var all []store.PluginInstallation
	for _, installation := range f.installations {
		all = append(all, *installation)
	}
	return all, nil
}

func (f *workerFake) PluginInstallation(_ context.Context, id int64) (store.PluginInstallation, error) {
	installation, found := f.installations[id]
	if !found {
		return store.PluginInstallation{}, store.ErrInstallationNotFound
	}
	if installation.State == store.InstallationPending && len(f.conclude) > 0 {
		installation.State, f.conclude = f.conclude[0], f.conclude[1:]
		installation.PluginID = "mse"
		if installation.State == store.InstallationUnrecognised {
			installation.Error = "no signature"
		}
	}
	return *installation, nil
}

func (f *workerFake) RequestPluginInstallation(_ context.Context, request store.InstallationRequest) (int64, error) {
	f.next++
	f.installations[f.next] = &store.PluginInstallation{
		ID: f.next, Reference: request.Reference, Digest: request.Digest, State: store.InstallationPending,
		Follows: request.Follows, ExpectedVersion: request.ExpectedVersion, Replaces: request.Replaces,
	}
	f.actions = append(f.actions, "request")
	return f.next, nil
}

func (f *workerFake) FailPluginInstallation(context.Context, int64, string) error { return nil }

func (f *workerFake) ActivatePluginInstallation(_ context.Context, id int64) error {
	f.installations[id].Active = true
	f.actions = append(f.actions, "activate")
	return nil
}

func (f *workerFake) AcceptSource(_ context.Context, pluginID, _, _ string) error {
	f.accepted = append(f.accepted, pluginID)
	f.actions = append(f.actions, "accept")
	return nil
}

func (f *workerFake) RetryPluginInstallation(_ context.Context, id int64) error {
	f.installations[id].State = store.InstallationPending
	return nil
}

func (f *workerFake) Enqueue(context.Context, store.JobRequest) (int64, error) { return 1, nil }

func (f *workerFake) Audit(context.Context, string, string, string, map[string]any) error { return nil }

func installer(fake *workerFake) Installer {
	return Installer{
		Installations: Installations{Store: fake, Logger: quiet()},
		Store:         fake, Actor: "config-apply", Poll: time.Millisecond, Wait: time.Second,
	}
}

func declaring(accept bool) configfile.Set {
	return configfile.Set{Instance: &configfile.Instance{Plugins: map[string]configfile.InstancePlugin{
		"mse": {Install: &configfile.Install{Reference: pinned, AcceptSource: accept}},
	}}}
}

func run(t *testing.T, fake *workerFake, set configfile.Set) error {
	t.Helper()
	ops, err := installer(fake).Installs(context.Background(), set)
	if err != nil {
		t.Fatalf("installs: %v", err)
	}
	return (Applier{}).Execute(context.Background(), ops)
}

func TestADeclaredPluginIsInstalledThenActivated(t *testing.T) {
	fake := &workerFake{installations: map[int64]*store.PluginInstallation{}, conclude: []string{store.InstallationInstalled}}
	if err := run(t, fake, declaring(false)); err != nil {
		t.Fatalf("install: %v", err)
	}
	if got := strings.Join(fake.actions, ","); got != "request,activate" {
		t.Errorf("actions = %s", got)
	}
	ops, _ := installer(fake).Installs(context.Background(), declaring(false))
	if len(ops) != 0 {
		t.Errorf("an active installation is installed again: %v", ops)
	}
}

func TestAnUnrecognisedSourceIsAcceptedOnlyWhenTheFileSaysSo(t *testing.T) {
	refused := &workerFake{installations: map[int64]*store.PluginInstallation{}, conclude: []string{store.InstallationUnrecognised}}
	err := run(t, refused, declaring(false))
	if err == nil || !strings.Contains(err.Error(), "acceptSource") {
		t.Errorf("error = %v, want one naming acceptSource", err)
	}
	if len(refused.accepted) != 0 {
		t.Errorf("a source was accepted without the file saying so")
	}

	accepted := &workerFake{installations: map[int64]*store.PluginInstallation{},
		conclude: []string{store.InstallationUnrecognised, store.InstallationInstalled}}
	if err := run(t, accepted, declaring(true)); err != nil {
		t.Fatalf("install: %v", err)
	}
	if got := strings.Join(accepted.actions, ","); got != "request,accept,activate" {
		t.Errorf("actions = %s", got)
	}
}

func TestAFailedInstallationIsRequestedAgain(t *testing.T) {
	fake := &workerFake{installations: map[int64]*store.PluginInstallation{
		7: {ID: 7, Reference: pinned, State: store.InstallationFailed},
	}, next: 7, conclude: []string{store.InstallationInstalled}}
	if err := run(t, fake, declaring(false)); err != nil {
		t.Fatalf("install: %v", err)
	}
	if got := strings.Join(fake.actions, ","); got != "request,activate" {
		t.Errorf("actions = %s", got)
	}
}
