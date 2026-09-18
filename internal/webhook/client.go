package webhook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// Options bounds what an outgoing call can reach and cost.
type Options struct {
	// AllowPrivate allows private targets and the loopback.
	//
	// False by default, and that is the right default: a production instance has no reason to call
	// its own network, and a blanket authorization is exactly what a forgery goes through. A local
	// deployment declares it, knowingly.
	//
	// It **never** reopens link-local: instance metadata is not a target a deployment wants to reach.
	AllowPrivate bool
	// AllowInsecure allows `http://`.
	//
	// False by default: a webhook payload carries a release's tag, sometimes a request's title, and
	// the token that authenticates it. In clear text, all of that crosses a readable network.
	AllowInsecure bool
	// Timeout bounds the whole call. A slow recipient must not hold back the plan engine: a stage's
	// steps start together, and a hung call would block the stage's conclusion.
	Timeout time.Duration
	// MaxResponseBytes bounds what is read back.
	//
	// The response serves diagnosis — a code, an excerpt of the body — and is not to be used. With no
	// bound, a hostile recipient would grow the worker's memory through an endless response.
	MaxResponseBytes int64
}

// Default values, applied to what is not declared.
const (
	defaultTimeout          = 15 * time.Second
	defaultMaxResponseBytes = 8 << 10
)

// Client issues outgoing calls towards controlled targets.
type Client struct {
	http    *http.Client
	options Options
}

// New builds a client whose transport refuses forbidden targets.
func New(options Options) *Client {
	if options.Timeout <= 0 {
		options.Timeout = defaultTimeout
	}
	if options.MaxResponseBytes <= 0 {
		options.MaxResponseBytes = defaultMaxResponseBytes
	}

	// The check lives here, in the dialler, and not in a URL check done upstream. The network stack
	// calls `Control` with the **resolved** address, just before connecting: there is no second
	// resolution to fool, and DNS rebinding gets no purchase.
	dialer := &net.Dialer{
		Timeout:   options.Timeout,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			return checkAddress(address, options.AllowPrivate)
		},
	}

	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   options.Timeout,
		ResponseHeaderTimeout: options.Timeout,
		// Connections are not reused between targets: a shared pool would mean an allowed target then
		// re-resolved to a forbidden address would borrow an already open connection, without passing
		// through the check again.
		DisableKeepAlives: true,
		Proxy:             nil,
	}

	return &Client{
		options: options,
		http: &http.Client{
			Transport: transport,
			Timeout:   options.Timeout,
			// No redirect is followed. A redirect is the obvious way around a check done on the starting URL:
			// the recipient answers 302 towards `169.254.169.254`, and an obliging client goes there.
			//
			// The dialler would refuse to connect, but failing **here** states the real cause rather than a
			// refused address nobody typed.
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				return fmt.Errorf("%w: redirect to %s refused",
					ErrBlockedTarget, safeURL(req.URL))
			},
		},
	}
}

// Response is what a call brought back.
//
// Enough to diagnose, no more: a code, and an excerpt of the body. The headers **sent** never
// appear in it — they carry the authentication, and this response ends up in a database the screen
// displays.
type Response struct {
	Status int
	// Body is truncated to [Options.MaxResponseBytes].
	Body string
}

// OK says the recipient accepted.
//
// Any 2xx code, and nothing else. A 3xx is not a success — the redirect not having been followed,
// nobody received the payload.
func (r Response) OK() bool { return r.Status >= 200 && r.Status < 300 }

// ErrInsecureTarget reports a cleartext target with no authorization.
var ErrInsecureTarget = errors.New("cible en clair")

// Post issues a request and returns what it brought back.
//
// `headers` carries the authentication. Its values are **never** logged nor returned: it is the
// counterpart of the rule that a sensitive field does not cross a template.
func (c *Client) Post(
	ctx context.Context, target string, headers map[string]string, body []byte,
) (Response, error) {
	parsed, err := c.checkURL(target)
	if err != nil {
		return Response{}, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(),
		bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("outgoing request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}

	response, err := c.http.Do(request)
	if err != nil {
		// An HTTP client's error message carries the URL. It may contain a token in its query string — a
		// common practice of chat webhooks — hence the redacted form.
		return Response{}, fmt.Errorf("call to %s: %w", safeURL(parsed), unwrapURLError(err))
	}
	defer response.Body.Close()

	extract, err := io.ReadAll(io.LimitReader(response.Body, c.options.MaxResponseBytes))
	if err != nil {
		return Response{Status: response.StatusCode}, nil
	}
	return Response{Status: response.StatusCode, Body: strings.TrimSpace(string(extract))}, nil
}

// checkURL refuses what must not even be dialled.
//
// The scheme is judged here and not in the dialler: the dialler sees only addresses, and would
// not be able to tell a cleartext call from an encrypted one.
func (c *Client) checkURL(target string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(target))
	if err != nil {
		return nil, fmt.Errorf("%w: URL illisible", ErrBlockedTarget)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("%w: URL with no host (%q)", ErrBlockedTarget, target)
	}

	switch parsed.Scheme {
	case "https":
	case "http":
		if !c.options.AllowInsecure {
			return nil, fmt.Errorf(
				"%w: %s — the payload and its token would cross the network in clear text",
				ErrInsecureTarget, safeURL(parsed))
		}
	default:
		return nil, fmt.Errorf("%w: scheme %q (expected https)", ErrBlockedTarget, parsed.Scheme)
	}

	return parsed, nil
}

// safeURL returns a URL that can be named in an error message.
//
// **`url.Redacted` is not enough.** It masks the password of the user part and leaves the query
// string intact — and that is precisely where chat webhooks carry their token:
// `https://hooks.example.fr/services/T00/B00/xxxxxxxx` in the path, or `?token=…` in the query.
//
// An error message ends up in a log, and a log is read with fewer precautions than a database.
// Only the scheme and the host therefore remain: enough to say which target failed, without
// carrying off what authorizes it.
func safeURL(target *url.URL) string {
	if target == nil {
		return ""
	}
	return target.Scheme + "://" + target.Host
}

// unwrapURLError frees the useful error from a `*url.Error`.
//
// Without it, a target refusal comes out wrapped in a message that repeats the URL, and
// `errors.Is(err, ErrBlockedTarget)` stops working for the caller — who would treat a refusal as
// a network outage.
func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}
	return err
}
