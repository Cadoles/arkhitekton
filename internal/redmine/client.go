package redmine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/provider"
)

/*
 * Access to Redmine, read-only and nothing else.
 *
 * The instance targeted is the **production** one: it carries the client's real requests, and
 * the dashboard has no reason to write to it. Creating a version, closing a request, posting a
 * note — all that stays a human act performed in Redmine.
 *
 * This prohibition is not entrusted to the callers' discipline. It is carried by the transport:
 * any request that is not a read is refused before reaching the network. A future caller
 * attempting a POST would get an immediate error, in development as in production, instead of
 * modifying a database of which no backup is held.
 *
 * Choosing the transport rather than a code review is deliberate: a review can miss a call, a
 * `RoundTrip` cannot be bypassed without rewriting the client's construction.
 */

// ErrWriteForbidden reports an attempt to write to Redmine.
//
// This error is not a normal runtime case to handle: it is the sign that a caller was written
// against this package's contract. It carries the method and the path so that the fault can be
// found, without ever carrying the key.
var ErrWriteForbidden = fmt.Errorf("redmine: %w", provider.ErrWriteForbidden)

// ErrNotConfigured reports a missing URL or API key.
var ErrNotConfigured = fmt.Errorf("redmine: %w", provider.ErrNotConfigured)

// readOnlyTransport forbids writing and carries the authentication.
//
// The two responsibilities are brought together deliberately: the key is added only here, so no
// caller has the opportunity to put it elsewhere — notably not in a query string, where it would
// end up in the server's access logs and in the `url.Error` of any failed request.
type readOnlyTransport struct {
	next   http.RoundTripper
	apiKey string
}

// Reading methods. `HEAD` is allowed because it changes nothing; it is not used today, but
// excluding it would force coming back here for a reachability test, which is exactly the kind of
// touch-up where the guard is dropped.
var readMethods = map[string]bool{
	http.MethodGet:  true,
	http.MethodHead: true,
}

func (t *readOnlyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if !readMethods[request.Method] {
		// Refusal before any network contact. The message names the method and the path, never the whole
		// request: a query string may carry request identifiers, and the principle stays not to copy into
		// a log what one did not choose to put there.
		return nil, fmt.Errorf("%w (%s %s)", ErrWriteForbidden, request.Method, request.URL.Path)
	}

	// The header, and not `?key=`: a query string is logged by the server, kept in intermediate
	// caches and copied into the Go client's error messages.
	//
	// The request is cloned: mutating the caller's is forbidden by the RoundTripper contract, and
	// would leave the key in an object the caller holds.
	authenticated := request.Clone(request.Context())
	authenticated.Header.Set("X-Redmine-API-Key", t.apiKey)
	authenticated.Header.Set("Accept", "application/json")

	return t.next.RoundTrip(authenticated)
}

// Client implements [provider.Tracker], and this assertion is not decorative: it makes a method
// removed from here, or a signature drifting from what the boundary announces, break at compile
// time rather than at the first call.
var _ provider.Tracker = (*Client)(nil)

// Client reads Redmine.
type Client struct {
	baseURL *url.URL
	http    *http.Client
}

// Options carries a client's configuration.
type Options struct {
	// URL de l'instance, par exemple `https://redmine.example.org`.
	URL string
	// APIKey is a Redmine account's API key. It must appear neither in a log, nor in an error
	// message, nor in a URL.
	APIKey string
	// Timeout bounds a call. Redmine under load sometimes answers in several seconds; with no bound,
	// a slow Redmine would become a slow dashboard.
	Timeout time.Duration
}

// New builds a read-only client.
//
// Returns [ErrNotConfigured] if the URL or the key is missing, rather than a client that would
// fail at the first call: the absence of configuration is a deployment decision, and the
// dashboard must be able to run without Redmine.
func New(options Options) (*Client, error) {
	if strings.TrimSpace(options.URL) == "" || strings.TrimSpace(options.APIKey) == "" {
		return nil, ErrNotConfigured
	}

	base, err := url.Parse(strings.TrimRight(options.URL, "/"))
	if err != nil {
		return nil, fmt.Errorf("redmine: invalid URL: %w", err)
	}
	if base.Scheme != "https" && base.Hostname() != "localhost" && base.Hostname() != "127.0.0.1" {
		// The key travels with every request. In clear text over the network, it is readable by any
		// intermediary, and this is access to production.
		return nil, fmt.Errorf("redmine: %s requires https", base.Redacted())
	}

	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	return &Client{
		baseURL: base,
		http: &http.Client{
			Timeout:   timeout,
			Transport: &readOnlyTransport{next: http.DefaultTransport, apiKey: options.APIKey},
		},
	}, nil
}

// get reads a resource and returns its body.
//
// The package's only network exit: every read passes through here, which gives a single place
// for handling the error codes, and a single place to re-read in order to check that no write is
// possible.
func (c *Client) get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	target := c.baseURL.JoinPath(path)
	if len(query) > 0 {
		target.RawQuery = query.Encode()
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("redmine: request: %w", err)
	}

	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("redmine: call to %s: %w", target.Redacted(), err)
	}
	defer response.Body.Close()

	switch {
	case response.StatusCode == http.StatusUnauthorized, response.StatusCode == http.StatusForbidden:
		// Told apart from the rest: it is almost always a key that is absent, revoked or without rights
		// on the project, and not a Redmine outage.
		return nil, fmt.Errorf("redmine: access refused on %s (%d) — invalid key or without rights on the project",
			target.Path, response.StatusCode)
	case response.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("redmine: %s not found", target.Path)
	case response.StatusCode >= 300:
		return nil, fmt.Errorf("redmine: %s answered %d", target.Path, response.StatusCode)
	}

	// 8 MiB: a paginated list of requests fits amply within it, and an unexpected response must not
	// be able to exhaust the process's memory.
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("redmine: reading the response from %s: %w", target.Path, err)
	}
	return body, nil
}

// CheckAccess checks the key and returns the account it belongs to.
//
// `/users/current.json` is the canonical read for validating an API key: it costs Redmine
// nothing and touches nothing. It is what allows saying "the key is good" without having to
// attempt a real operation.
//
// The `admin` field is returned because it deserves to be seen: an administrator key gives far
// more than the dashboard needs. The read-only transport stops it being abused from here, but an
// over-powerful key stays an over-powerful key — better to know it and ask for a restricted
// account.
func (c *Client) CheckAccess(ctx context.Context) (provider.Account, error) {
	body, err := c.get(ctx, "/users/current.json", nil)
	if err != nil {
		return provider.Account{}, err
	}

	// A wire structure, distinct from the boundary type: the field names are Redmine's, and exposing
	// them would force any other implementation to take them on.
	var payload struct {
		User struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
			Admin bool   `json:"admin"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return provider.Account{}, fmt.Errorf("redmine: response from /users/current.json unreadable: %w", err)
	}
	return provider.Account{
		ID: payload.User.ID, Login: payload.User.Login, Admin: payload.User.Admin,
	}, nil
}
