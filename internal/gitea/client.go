package gitea

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/provider"
)

/*
 * Access to the Gitea forge, read-only and nothing else.
 *
 * The same contract as `internal/redmine`, for the same reason: the prohibition on writing is not
 * entrusted to the callers' discipline but to the transport, which refuses any method other than
 * GET and HEAD before any network contact. The day the dashboard has to write to the forge — set
 * a tag, publish a Gitea release — it will be a distinct client and an explicit decision, not a
 * relaxation of this one.
 */

// The contract's sentinels, wrapped so the log names the tool and `errors.Is` still
// recognises the sentinel.
var (
	// ErrWriteForbidden reports an attempted write to the forge.
	ErrWriteForbidden = fmt.Errorf("gitea: %w", provider.ErrWriteForbidden)
	// ErrNotConfigured reports a missing URL, token or repository.
	ErrNotConfigured = fmt.Errorf("gitea: %w", provider.ErrNotConfigured)
)

var readMethods = map[string]bool{
	http.MethodGet:  true,
	http.MethodHead: true,
}

// readOnlyTransport forbids writing and carries authentication.
//
// The token is added only here: no caller has the chance to put it in a URL, where it
// would end up in access logs and error messages.
type readOnlyTransport struct {
	next  http.RoundTripper
	token string
}

func (t *readOnlyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if !readMethods[request.Method] {
		return nil, fmt.Errorf("%w (%s %s)", ErrWriteForbidden, request.Method, request.URL.Path)
	}

	authenticated := request.Clone(request.Context())
	authenticated.Header.Set("Authorization", "token "+t.token)
	authenticated.Header.Set("Accept", "application/json")
	return t.next.RoundTrip(authenticated)
}

// Client implements [provider.Forge], Prober [provider.RepositoryProber], and Writer
// [provider.TagWriter]. The three assertions hold the contract at compile time: a
// boundary nobody checks drifts from its implementations with nothing saying so,
// which is precisely what it is meant to prevent.
var (
	_ provider.Forge            = (*Client)(nil)
	_ provider.RepositoryProber = Prober{}
	_ provider.TagWriter        = (*Writer)(nil)
)

// Client queries the forge for reading.
type Client struct {
	baseURL *url.URL
	// owner and repo are fixed at construction: this client reads the application's
	// repository, not the whole forge.
	owner string
	repo  string
	http  *http.Client
}

// Options carries a client's configuration.
type Options struct {
	// The forge's URL, for example `https://forge.cadoles.com`.
	URL string
	// Token is a forge API token. It must appear in no log, no error message and no URL.
	Token string
	// Repository au format `owner/nom`, par exemple `CNOUS/mse`.
	Repository string
	Timeout    time.Duration
}

// New builds a read-only client.
func New(options Options) (*Client, error) {
	if strings.TrimSpace(options.URL) == "" || strings.TrimSpace(options.Token) == "" ||
		strings.TrimSpace(options.Repository) == "" {
		return nil, ErrNotConfigured
	}

	owner, repo, found := strings.Cut(strings.TrimSpace(options.Repository), "/")
	if !found || owner == "" || repo == "" {
		return nil, fmt.Errorf("gitea: invalid repository %q, expected owner/name", options.Repository)
	}

	base, err := url.Parse(strings.TrimRight(options.URL, "/"))
	if err != nil {
		return nil, fmt.Errorf("gitea: URL invalide: %w", err)
	}
	if base.Scheme != "https" && base.Hostname() != "localhost" && base.Hostname() != "127.0.0.1" {
		// The token travels on every request, and it opens the whole forge in its holder's
		// name — not only this repository.
		return nil, fmt.Errorf("gitea: %s requires https", base.Redacted())
	}

	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	return &Client{
		baseURL: base,
		owner:   owner,
		repo:    repo,
		http: &http.Client{
			Timeout:   timeout,
			Transport: &readOnlyTransport{next: http.DefaultTransport, token: options.Token},
		},
	}, nil
}

// get reads an API resource. The package's only network exit.
func (c *Client) get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	target := c.baseURL.JoinPath("/api/v1" + path)
	if len(query) > 0 {
		target.RawQuery = query.Encode()
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("gitea: request: %w", err)
	}

	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("gitea: call to %s: %w", target.Redacted(), err)
	}
	defer response.Body.Close()

	switch {
	case response.StatusCode == http.StatusUnauthorized, response.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("gitea: access refused on %s (%d) — invalid token, or no right on the repository",
			target.Path, response.StatusCode)
	case response.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("gitea: %s not found — does repository %s/%s exist?",
			target.Path, c.owner, c.repo)
	case response.StatusCode >= 300:
		return nil, fmt.Errorf("gitea: %s answered %d", target.Path, response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("gitea: reading the response from %s: %w", target.Path, err)
	}
	return body, nil
}

// pageSize is the page size requested.
//
// Fifty is the default of the forge's configurable ceiling: asking for more would be
// silently truncated to that ceiling, and pagination by "short page" would believe
// the list finished.
const pageSize = 50

// maxRefs bounds the total number of references read.
//
// The form offers a list; nobody chooses among thousands of entries, and tags arrive
// newest first — the first hundred cover the useful history. The bound is also a
// latency bound: each page is a round trip to the forge, and the real repository
// carries more than three hundred tags. Crossing it is reported by the caller rather
// than truncated silently, and free-text input stays open for a reference beyond.
const maxRefs = 100

// DefaultBranch returns the repository's default branch.
//
// It is the value the form offers by default: the common case is building from the
// main branch, and asking the forge avoids assuming `main` where the existing code is
// on `develop`.
func (c *Client) DefaultBranch(ctx context.Context) (string, error) {
	body, err := c.get(ctx, "/repos/"+c.owner+"/"+c.repo, nil)
	if err != nil {
		return "", err
	}

	var payload struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("gitea: repository unreadable: %w", err)
	}
	return payload.DefaultBranch, nil
}

// ListBranches returns the repository's branches, all pages together.
func (c *Client) ListBranches(ctx context.Context) ([]provider.Ref, bool, error) {
	return c.listRefs(ctx, "/branches", func(body []byte) ([]provider.Ref, error) {
		var payload []struct {
			Name   string `json:"name"`
			Commit struct {
				ID string `json:"id"`
			} `json:"commit"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("gitea: branch list unreadable: %w", err)
		}
		refs := make([]provider.Ref, 0, len(payload))
		for _, raw := range payload {
			refs = append(refs, provider.Ref{Name: raw.Name, SHA: raw.Commit.ID})
		}
		return refs, nil
	})
}

// ListTags returns the repository's tags, newest first.
func (c *Client) ListTags(ctx context.Context) ([]provider.Ref, bool, error) {
	return c.listRefs(ctx, "/tags", func(body []byte) ([]provider.Ref, error) {
		var payload []struct {
			Name   string `json:"name"`
			Commit struct {
				SHA string `json:"sha"`
			} `json:"commit"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("gitea: tag list unreadable: %w", err)
		}
		refs := make([]provider.Ref, 0, len(payload))
		for _, raw := range payload {
			refs = append(refs, provider.Ref{Name: raw.Name, SHA: raw.Commit.SHA})
		}
		return refs, nil
	})
}

// listRefs pages until the short page, the bound, or the empty page.
func (c *Client) listRefs(ctx context.Context, path string, decode func([]byte) ([]provider.Ref, error)) ([]provider.Ref, bool, error) {
	var refs []provider.Ref
	for page := 1; ; page++ {
		query := url.Values{}
		query.Set("page", strconv.Itoa(page))
		query.Set("limit", strconv.Itoa(pageSize))

		body, err := c.get(ctx, "/repos/"+c.owner+"/"+c.repo+path, query)
		if err != nil {
			return nil, false, err
		}
		batch, err := decode(body)
		if err != nil {
			return nil, false, err
		}
		refs = append(refs, batch...)

		if len(batch) < pageSize {
			return refs, false, nil
		}
		if len(refs) >= maxRefs {
			return refs[:maxRefs], true, nil
		}
	}
}

// CheckAccess verifies the token and returns the account it belongs to.
//
// The same role as for the tracker: knowing a token works without waiting for a form
// to use it, and seeing whether it belongs to an administrator account — an
// over-powerful token stays over-powerful even unused.
func (c *Client) CheckAccess(ctx context.Context) (provider.Account, error) {
	body, err := c.get(ctx, "/user", nil)
	if err != nil {
		return provider.Account{}, err
	}

	// A wire structure, distinct from the boundary type: `is_admin` is a name specific to
	// this forge, and exposing it would force any other implementation to adopt it. The
	// identifier is not read — the dashboard does nothing with it.
	var account struct {
		Login string `json:"login"`
		Admin bool   `json:"is_admin"`
	}
	if err := json.Unmarshal(body, &account); err != nil {
		return provider.Account{}, fmt.Errorf("gitea: /user response unreadable: %w", err)
	}
	return provider.Account{Login: account.Login, Admin: account.Admin}, nil
}
