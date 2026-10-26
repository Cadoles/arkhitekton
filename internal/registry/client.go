// Package registry reads the image registry through the OCI distribution API.
//
// It only observes. The dashboard never pushes an image: promotion re-tags through a
// Task carrying its own credentials. The account used here needs read access only,
// and granting it write would give the dashboard a power it does not exercise.
//
// What the registry gives that nothing else does: the list of tags carried by a
// digest. A promotion being a re-tag of the same digest, that list says both which
// version an image is and where it was promoted — including for production, whose
// cluster is out of reach.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
)

// Manifest types requested.
//
// Both forms coexist in a real registry: older images are plain v2 manifests, recent
// ones multi-architecture lists. Requesting only one
var acceptManifests = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// Client queries a registry.
type Client struct {
	base     *url.URL
	username string
	password string
	http     *http.Client
	// project is the repository prefix, without the host.
	project string
}

// ErrNotConfigured reports missing credentials.
//
// Distinct from an access error: the dashboard must be able to run without a
// registry — the image inventory is then missing, the rest works — and the interface
// must be able to say so instead of showing a fault.
var ErrNotConfigured = errors.New("registry not configured")

// Open builds the client. Returns ErrNotConfigured when credentials are missing.
func Open(cfg config.RegistryConfig) (*Client, error) {
	if strings.TrimSpace(cfg.URL) == "" || strings.TrimSpace(cfg.Username) == "" || strings.TrimSpace(cfg.Password) == "" {
		return nil, ErrNotConfigured
	}

	base, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid registry URL %q: %w", cfg.URL, err)
	}

	// `registry.project` carries the host because that is the form it takes in pipeline
	// parameters. The registry's API expects only the path.
	project := cfg.Project
	if _, path, found := strings.Cut(project, "/"); found {
		project = path
	}

	return &Client{
		base:     base,
		username: cfg.Username,
		password: cfg.Password,
		project:  project,
		http:     &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// Project returns the repository prefix, without the host.
func (c *Client) Project() string { return c.project }

// Tags lists a repository's tags, in the order the registry returns them.
func (c *Client) Tags(ctx context.Context, repository string) ([]string, error) {
	var payload struct {
		Tags []string `json:"tags"`
	}

	if err := c.getJSON(ctx, fmt.Sprintf("/v2/%s/tags/list", c.path(repository)), &payload); err != nil {
		return nil, err
	}
	return payload.Tags, nil
}

// Digest returns a tag's digest.
//
// It is read from the `Docker-Content-Digest` header of a HEAD request: asking for
// the whole manifest to keep only the digest would download kilobytes per tag, and a
// repository has hundreds.
func (c *Client) Digest(ctx context.Context, repository, tag string) (string, error) {
	request, err := c.request(ctx, http.MethodHead,
		fmt.Sprintf("/v2/%s/manifests/%s", c.path(repository), url.PathEscape(tag)))
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", acceptManifests)

	response, err := c.do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("digest of %s:%s: status %d", repository, tag, response.StatusCode)
	}

	digest := response.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("digest of %s:%s: header missing", repository, tag)
	}
	return digest, nil
}

// TagsByDigest indexes a repository's tags by digest.
//
// This is the operation that makes the registry worth reading: a promotion being a
// re-tag of the same digest, a digest's tags say both which version it is and where
// it was promoted. Without it, every environment would have to be queried —
// impossible for production.
//
// The cost is one HEAD request per tag. `limit` bounds the sweep to the most recent
// tags the registry returns: a repository with hundreds of tags would otherwise take
// minutes, for information that only concerns live versions.
func (c *Client) TagsByDigest(ctx context.Context, repository string, limit int) (map[string][]string, error) {
	tags, err := c.Tags(ctx, repository)
	if err != nil {
		return nil, err
	}

	if limit > 0 && len(tags) > limit {
		// Registries return tags in ascending alphabetical order: the end of the list is the
		// most recent, for a dated scheme as for SemVer.
		tags = tags[len(tags)-limit:]
	}

	byDigest := make(map[string][]string, len(tags))
	for _, tag := range tags {
		if ctx.Err() != nil {
			return byDigest, ctx.Err()
		}

		digest, err := c.Digest(ctx, repository, tag)
		if err != nil {
			// An unreadable tag — deleted between the listing and the resolution, or a manifest
			// of an unexpected type — must not cost the others.
			continue
		}
		byDigest[digest] = append(byDigest[digest], tag)
	}
	return byDigest, nil
}

func (c *Client) path(repository string) string {
	if c.project == "" {
		return repository
	}
	return c.project + "/" + repository
}

func (c *Client) request(ctx context.Context, method, path string) (*http.Request, error) {
	target := *c.base
	target.Path = path

	request, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("request to %s: %w", path, err)
	}
	return request, nil
}

// do runs the request, handling authentication.
//
// Two schemes coexist across registries: Basic accepted directly, or a bearer token
// to be obtained from an authorization service named by the `WWW-Authenticate`
// header. Handling only the first works today and would fail on a registry that
// changes configuration — for the cost of twenty lines.
func (c *Client) do(request *http.Request) (*http.Response, error) {
	request.SetBasicAuth(c.username, c.password)

	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call to registry %s: %w", c.base.Host, err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		return response, nil
	}

	challenge := response.Header.Get("WWW-Authenticate")
	_ = response.Body.Close()

	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		// The registry refuses Basic: the credential is at fault, not the scheme. Returning
		// the response as is lets the caller produce a message saying so.
		return c.retry(request)
	}

	token, err := c.token(request.Context(), challenge)
	if err != nil {
		return nil, err
	}

	retried := request.Clone(request.Context())
	retried.Header.Set("Authorization", "Bearer "+token)

	response, err = c.http.Do(retried)
	if err != nil {
		return nil, fmt.Errorf("call to registry %s: %w", c.base.Host, err)
	}
	return response, nil
}

func (c *Client) retry(request *http.Request) (*http.Response, error) {
	retried := request.Clone(request.Context())
	retried.SetBasicAuth(c.username, c.password)

	response, err := c.http.Do(retried)
	if err != nil {
		return nil, fmt.Errorf("call to registry %s: %w", c.base.Host, err)
	}
	return response, nil
}

// token obtains a bearer token from the service the challenge names.
func (c *Client) token(ctx context.Context, challenge string) (string, error) {
	parameters := parseChallenge(challenge)

	realm := parameters["realm"]
	if realm == "" {
		return "", fmt.Errorf("authentication challenge without a realm: %q", challenge)
	}

	target, err := url.Parse(realm)
	if err != nil {
		return "", fmt.Errorf("invalid realm %q: %w", realm, err)
	}

	query := target.Query()
	for _, name := range []string{"service", "scope"} {
		if value := parameters[name]; value != "" {
			query.Set(name, value)
		}
	}
	target.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	request.SetBasicAuth(c.username, c.password)

	response, err := c.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token request refused: status %d", response.StatusCode)
	}

	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return "", fmt.Errorf("unreadable token: %w", err)
	}

	// Both names circulate: `token` in the Docker specification, `access_token` in
	// implementations aligned on OAuth2.
	if payload.Token != "" {
		return payload.Token, nil
	}
	if payload.AccessToken != "" {
		return payload.AccessToken, nil
	}
	return "", errors.New("the authorization service returned no token")
}

// parseChallenge splits a Bearer `WWW-Authenticate` header.
func parseChallenge(challenge string) map[string]string {
	parameters := make(map[string]string)

	rest := strings.TrimSpace(challenge)
	if index := strings.Index(rest, " "); index >= 0 {
		rest = rest[index+1:]
	}

	for _, part := range strings.Split(rest, ",") {
		name, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			continue
		}
		parameters[strings.TrimSpace(name)] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	return parameters
}

func (c *Client) getJSON(ctx context.Context, path string, target any) error {
	request, err := c.request(ctx, http.MethodGet, path)
	if err != nil {
		return err
	}

	response, err := c.do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: status %d", path, response.StatusCode)
	}

	// A read bound: a compromised or failing registry must not be able to inflate the
	// worker's memory.
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(target); err != nil {
		return fmt.Errorf("%s: unreadable response: %w", path, err)
	}
	return nil
}
