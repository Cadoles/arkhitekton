package configure

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/configfile"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/installed"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The plugins a configuration installs.
 *
 * Installed before anything else is planned: a plugin brings settings, facts, actions and kinds the
 * rest of the configuration may name, and none of them can be checked before it is there. The
 * caller applies the installations, rebuilds its registry, then plans the rest.
 */

// InstallStore is what following an installation needs from the database.
type InstallStore interface {
	PluginInstallations(ctx context.Context) ([]store.PluginInstallation, error)
	PluginInstallation(ctx context.Context, id int64) (store.PluginInstallation, error)
	PluginSources(ctx context.Context) ([]store.PluginSource, error)
}

// Installer installs and activates the plugins a configuration declares.
type Installer struct {
	Installations Installations
	Store         InstallStore
	Actor         string
	// Wait bounds how long an installation may take; Poll is how often it is looked at.
	Wait time.Duration
	Poll time.Duration
}

// Installs returns the operations that make the declared plugins the active ones. Empty when every
// declared plugin is installed as declared.
//
// A repository alone installs the plugin if it is not installed from there, and makes it follow
// that repository; it never changes the version: an update is decided on the screen, and an apply
// replayed must not move a version by itself. A digest or a tag pins: another version of the same
// repository is replaced by it, the file being the decision.
func (i Installer) Installs(ctx context.Context, desired configfile.Set) ([]Op, error) {
	if desired.Instance == nil {
		return nil, nil
	}
	installations, err := i.Store.PluginInstallations(ctx)
	if err != nil {
		return nil, err
	}
	sources, err := i.Store.PluginSources(ctx)
	if err != nil {
		return nil, err
	}
	sourceOf := map[string]store.PluginSource{}
	for _, source := range sources {
		sourceOf[source.PluginID] = source
	}

	var ops []Op
	for _, id := range sortedKeys(desired.Instance.Plugins) {
		declared := desired.Instance.Plugins[id].Install
		if declared == nil {
			continue
		}
		wanted, err := installed.ParseSource(declared.Reference)
		if err != nil {
			return nil, fmt.Errorf("plugin %s: %w", id, err)
		}
		accept := declared.AcceptSource
		current, found := sourceOf[id]
		running := found && current.ActiveID != 0 && current.Repository == wanted.Repository

		switch {
		case !wanted.Pinned() && running && !current.Pinned:
			continue
		case !wanted.Pinned() && running:
			ops = append(ops, Op{
				Description: "~ plugin " + id + " follows " + wanted.Repository,
				run: func(ctx context.Context) error {
					return i.Installations.SetSource(ctx, i.Actor, id, false)
				},
			})
			continue
		case wanted.Pinned() && running && pinnedAt(installations, current, wanted):
			if !current.Pinned {
				ops = append(ops, Op{
					Description: "~ plugin " + id + " pinned to " + current.ActiveVersion,
					run: func(ctx context.Context) error {
						return i.Installations.SetSource(ctx, i.Actor, id, true)
					},
				})
			}
			continue
		}

		reference := declared.Reference
		if wanted.Digest != "" {
			reference = wanted.Reference()
		}
		mode, verb := ModeInstall, "+ install plugin "
		if running {
			mode, verb = ModeUpdate, "~ update plugin "
		}
		latest, resumable := latestOf(installations, reference)
		ops = append(ops, Op{
			Description: verb + id + " from " + reference,
			run: func(ctx context.Context) error {
				return i.install(ctx, id, reference, mode, latest, resumable, accept)
			},
		})
	}
	return ops, nil
}

// pinnedAt says whether the active version is the one a pinned declaration names: its digest, or
// its tag's version.
func pinnedAt(installations []store.PluginInstallation, current store.PluginSource, wanted installed.Source) bool {
	if wanted.Tag != "" {
		return current.ActiveVersion == wanted.Tag
	}
	for _, installation := range installations {
		if installation.ID == current.ActiveID {
			return installation.Digest == wanted.Digest
		}
	}
	return false
}

func latestOf(installations []store.PluginInstallation, reference string) (store.PluginInstallation, bool) {
	var matching []store.PluginInstallation
	for _, installation := range installations {
		if installation.Reference == reference && installation.State != store.InstallationRemoved {
			matching = append(matching, installation)
		}
	}
	if len(matching) == 0 {
		return store.PluginInstallation{}, false
	}
	return slices.MaxFunc(matching, func(a, b store.PluginInstallation) int { return int(a.ID - b.ID) }), true
}

// install takes an installation from wherever it stands to active: requested if there is none to
// resume, pulled by the worker, accepted when its source is unknown and the file says so, activated.
func (i Installer) install(
	ctx context.Context, id, reference string, mode Mode, latest store.PluginInstallation, found, accept bool,
) error {
	installationID := latest.ID
	if !found || latest.State == store.InstallationFailed || latest.State == store.InstallationRemoved || latest.Active {
		requested, _, _, err := i.Installations.Request(ctx, i.Actor, reference, mode)
		if err != nil {
			return err
		}
		installationID = requested
	}

	for {
		installation, err := i.wait(ctx, installationID)
		if err != nil {
			return err
		}
		if installation.PluginID != "" && installation.PluginID != id {
			return fmt.Errorf("the artifact carries plugin %q, the configuration declares it as %q", installation.PluginID, id)
		}
		switch installation.State {
		case store.InstallationInstalled:
			if installation.Active {
				return nil
			}
			return i.Installations.Activate(ctx, i.Actor, installationID)
		case store.InstallationUnrecognised:
			if !accept {
				return fmt.Errorf("the instance does not recognise its source (%s): set acceptSource to accept it", installation.Error)
			}
			if _, _, err := i.Installations.Accept(ctx, i.Actor, installationID); err != nil {
				return err
			}
			accept = false
		default:
			return fmt.Errorf("installation %d %s: %s", installationID, installation.State, installation.Error)
		}
	}
}

// wait returns the installation once the worker has concluded on it.
func (i Installer) wait(ctx context.Context, id int64) (store.PluginInstallation, error) {
	poll, wait := i.Poll, i.Wait
	if poll <= 0 {
		poll = 2 * time.Second
	}
	if wait <= 0 {
		wait = 10 * time.Minute
	}
	deadline := time.Now().Add(wait)
	for {
		installation, err := i.Store.PluginInstallation(ctx, id)
		if err != nil {
			return installation, err
		}
		if installation.State != store.InstallationPending {
			return installation, nil
		}
		if time.Now().After(deadline) {
			return installation, fmt.Errorf("installation %d still pending after %s: is the worker running?", id, wait)
		}
		select {
		case <-ctx.Done():
			return installation, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// PendingPlugins names the plugins the operations install or update: what the rest of the
// configuration waits for. Following or pinning installs nothing.
func PendingPlugins(ops []Op) []string {
	var ids []string
	for _, op := range ops {
		for _, prefix := range []string{"+ install plugin ", "~ update plugin "} {
			if rest, found := strings.CutPrefix(op.Description, prefix); found {
				id, _, _ := strings.Cut(rest, " ")
				ids = append(ids, id)
			}
		}
	}
	return ids
}
