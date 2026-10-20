package store

import (
	"context"
	"fmt"
)

/*
 * Directory: from the OIDC subject to the name one reads.
 *
 * The action records — creating a release, queueing a job, adding a request to a note — record
 * the **subject**, that is, `121`. It is the right choice on the storage side: the subject is
 * stable, a user name changes. But it is unreadable on screen, where the reader looks for "who",
 * not "which identifier".
 *
 * The resolution therefore happens at read time, and not by recording the name beside the
 * subject. Two reasons: the records already written become readable again with no migration, and
 * a renamed user is renamed everywhere at once rather than on their future actions only.
 */

// Directory maps known subjects to their names.
//
// Returned in one go rather than subject by subject: the table holds a few dozen rows, and
// one read per actor of a list of fifty releases would cost fifty round trips for the same
// result.
func (db *DB) Directory(ctx context.Context) (map[string]string, error) {
	rows, err := db.pool.Query(ctx,
		`select subject, coalesce(username, '') from user_roles where username <> ''`)
	if err != nil {
		return nil, fmt.Errorf("directory: %w", err)
	}
	defer rows.Close()

	names := map[string]string{}
	for rows.Next() {
		var subject, username string
		if err := rows.Scan(&subject, &username); err != nil {
			return nil, fmt.Errorf("directory: %w", err)
		}
		names[subject] = username
	}
	return names, rows.Err()
}
