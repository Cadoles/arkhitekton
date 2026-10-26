package http

import (
	"context"
	"log/slog"
)

/*
 * An actor's name, for display.
 *
 * The audit records the OIDC subject — `121` — because it is stable. The reader, though, looks
 * for "who". The translation happens here, at read time: the records already written become
 * readable again with no migration, and a renamed user is renamed everywhere at once.
 */

// DirectoryReader is what the translation needs from the database.
type DirectoryReader interface {
	Directory(ctx context.Context) (map[string]string, error)
}

// actors translates subjects into names.
//
// Loaded once per request and not per actor: a list of fifty releases would otherwise
// query the database fifty times for the same directory.
type actors struct {
	names map[string]string
}

// resolveActors loads the directory.
//
// A failure does not fail the screen: the subjects are then displayed as they are, which
// is the previous behaviour. Losing a whole page because a name could not be read would be
// disproportionate — but so would hiding it, hence the log.
func resolveActors(ctx context.Context, source DirectoryReader, logger *slog.Logger) actors {
	names, err := source.Directory(ctx)
	if err != nil {
		if logger != nil {
			logger.Error("user directory", "error", err)
		}
		return actors{}
	}
	return actors{names: names}
}

// name returns a subject's name, or the subject itself.
//
// The fallback to the subject is deliberate: an actor unknown to the directory — a service
// account, a deleted user, an action predating the roles table — must stay identifiable. An
// empty string would erase the record of who acted, which is exactly what an audit log must
// never do.
func (a actors) name(subject string) string {
	if name, ok := a.names[subject]; ok && name != "" {
		return name
	}
	return subject
}
