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
	f.recordSource(f.installations[id])
	return nil
}

// recordSource records the active version's source, as completing or activating an installation
// does.
func (f *workerFake) recordSource(installation *store.PluginInstallation) {
	if f.sources == nil {
		f.sources = map[string]store.PluginSource{}
	}
	repository, _, _ := strings.Cut(installation.Reference, "@")
	f.sources[installation.PluginID] = store.PluginSource{
		PluginID: installation.PluginID, Repository: repository, Pinned: !installation.Follows,
		ActiveID: installation.ID, ActiveVersion: "1.0.0",
	}
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

const repository = "registry.example/mse"

func declaringReference(reference string) configfile.Set {
	return configfile.Set{Instance: &configfile.Instance{Plugins: map[string]configfile.InstancePlugin{
		"mse": {Install: &configfile.Install{Reference: reference}},
	}}}
}

// running is a fake whose plugin is installed and active from the repository.
func running(follows bool) *workerFake {
	fake := &workerFake{installations: map[int64]*store.PluginInstallation{
		2: {ID: 2, PluginID: "mse", Reference: pinned, Digest: pinned[len(repository)+1:], State: store.InstallationInstalled,
			Active: true, Follows: follows},
	}, next: 2}
	fake.recordSource(fake.installations[2])
	return fake
}

// A repository alone installs the plugin and makes it follow the repository.
func TestADeclaredRepositoryInstallsAPluginThatFollowsIt(t *testing.T) {
	fake := &workerFake{installations: map[int64]*store.PluginInstallation{}, conclude: []string{store.InstallationInstalled}}
	if err := run(t, fake, declaringReference(repository)); err != nil {
		t.Fatal(err)
	}
	if requested := fake.installations[1]; requested.Reference != repository || !requested.Follows || requested.Digest != "" {
		t.Errorf("requested = %+v", requested)
	}
}

// Declared by repository, a plugin installed from it keeps its version: an apply follows, it does
// not update.
func TestADeclaredRepositoryFollowsWithoutUpdating(t *testing.T) {
	following := running(true)
	if ops, _ := installer(following).Installs(context.Background(), declaringReference(repository)); len(ops) != 0 {
		t.Errorf("ops = %v", ops)
	}

	pinnedPlugin := running(false)
	if ops, _ := installer(pinnedPlugin).Installs(context.Background(), declaringReference(repository)); len(PendingPlugins(ops)) != 0 {
		t.Errorf("following installs nothing the rest must wait for: %v", PendingPlugins(ops))
	}
	if err := run(t, pinnedPlugin, declaringReference(repository)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(pinnedPlugin.actions, ","); got != "pin false" {
		t.Errorf("actions = %s, expected the plugin to follow its repository and nothing installed", got)
	}
}

// Another digest of the same repository replaces the active version; the same digest changes
// nothing.
func TestADeclaredDigestReplacesAnotherVersion(t *testing.T) {
	if ops, _ := installer(running(false)).Installs(context.Background(), declaring(false)); len(ops) != 0 {
		t.Errorf("the declared digest is the active one: %v", ops)
	}

	fake := running(false)
	fake.conclude = []string{store.InstallationInstalled}
	other := repository + "@sha256:" + strings.Repeat("f", 64)
	ops, err := installer(fake).Installs(context.Background(), declaringReference(other))
	if err != nil || len(ops) != 1 || !strings.HasPrefix(ops[0].Description, "~ update plugin mse") {
		t.Fatalf("ops = %v, %v", ops, err)
	}
	if pending := PendingPlugins(ops); len(pending) != 1 || pending[0] != "mse" {
		t.Errorf("pending = %v: the rest waits for the update", pending)
	}
	if err := (Applier{}).Execute(context.Background(), ops); err != nil {
		t.Fatal(err)
	}
	if requested := fake.installations[3]; requested.Replaces == nil || *requested.Replaces != 2 || requested.Follows {
		t.Errorf("requested = %+v", requested)
	}
}

// A plugin moving to another repository whose version is already installed beside the active one:
// the apply activates that installation rather than asking for it again. Once resolved, it carries
// `repository@digest`, not the repository the file names.
func TestARepositoryAlreadyInstalledBesideIsActivated(t *testing.T) {
	fake := running(false)
	other := "registry.local/mse"
	fake.installations[3] = &store.PluginInstallation{
		ID: 3, PluginID: "mse", Reference: other + "@sha256:" + strings.Repeat("e", 64),
		Digest: "sha256:" + strings.Repeat("e", 64), State: store.InstallationInstalled, Follows: true,
	}
	fake.next = 3
	if err := run(t, fake, declaringReference(other)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fake.actions, ","); got != "activate" {
		t.Errorf("actions = %s, expected the installation beside to be activated", got)
	}
}
