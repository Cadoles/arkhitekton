package store

import (
	"errors"
	"net/url"
	"strings"
)

// redactError removes the DSN's password from an error message.
//
// pgx readily quotes the connection string in its parse errors. Those errors end up in logs,
// sometimes in a ticket, sometimes in a screenshot. The password has no business there, and
// it is precisely during an incident that people copy and paste without re-reading.
//
// The message is otherwise kept: masking the whole error would make diagnosis impossible at
// the moment it is most needed.
func redactError(err error, dsn string) error {
	if err == nil {
		return nil
	}

	password := passwordFromDSN(dsn)
	if password == "" {
		return err
	}

	message := err.Error()
	if !strings.Contains(message, password) {
		return err
	}

	return errors.New(strings.ReplaceAll(message, password, "xxxxx"))
}

// RedactError removes the DSN's password from an error message, for migrations kept outside
// this package.
func RedactError(err error, dsn string) error { return redactError(err, dsn) }

// passwordFromDSN extracts the password from a connection URL. Returns an empty string for
// key-value DSNs (`host=... password=...`), which this function does not claim to cover:
// pgxpool.ParseConfig accepts them, but the dashboard's deployment uses the URL form.
func passwordFromDSN(dsn string) string {
	parsed, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil || parsed.User == nil {
		return ""
	}
	password, hasPassword := parsed.User.Password()
	if !hasPassword {
		return ""
	}
	return password
}
