package gitea

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/provider"
	"net/url"
	"strings"
	"time"
)

/*
 * Écriture sur la forge : ce qui s'ajoute, jamais ce qui s'enlève.
 *
 * Client **distinct** de celui en lecture, comme annoncé quand celui-ci a été
 * écrit. La séparation n'est pas cosmétique : le transport de lecture refuse tout
 * ce qui n'est pas GET ou HEAD, et l'assouplir aurait fait qu'un appel de lecture
 * mal écrit puisse écrire. Ici la permission est explicite, et son périmètre tient
 * dans une liste de méthodes HTTP.
 *
 * Trois gestes aujourd'hui : poser un tag, poser un statut de commit, ajouter un
 * commentaire. Les deux derniers viennent des actions que le plugin Gitea déclare.
 * Ils partagent une propriété qui vaut d'être nommée : **ils ajoutent**. Un statut
 * remplace le précédent de même contexte, un commentaire s'ajoute à un fil ; ni
 * l'un ni l'autre ne fait disparaître ce qui était là.
 *
 * `DELETE` reste refusé, et `PATCH` avec lui : poser un tag est réparable — on en
 * pose un autre — tandis qu'en supprimer un détruit la référence d'un livrable,
 * geste qu'aucun automate ne devrait pouvoir faire (B3). La même prudence vaut
 * pour un commentaire d'autrui qu'une action mal réglée pourrait réécrire.
 */

// ErrTagExists reports a tag already set. It wraps the contract's sentinel: setting a
// tag again after a partial failure is a normal case, and the caller must be able to
// recognise it without knowing which forge it is talking to.
var ErrTagExists = fmt.Errorf("gitea: %w", provider.ErrTagExists)

var writeMethods = map[string]bool{
	http.MethodGet:  true,
	http.MethodHead: true,
	http.MethodPost: true,
}

type writeTransport struct {
	next  http.RoundTripper
	token string
}

func (t *writeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if !writeMethods[request.Method] {
		return nil, fmt.Errorf("%w (%s %s): this client adds, it neither removes nor rewrites",
			ErrWriteForbidden, request.Method, request.URL.Path)
	}

	authenticated := request.Clone(request.Context())
	authenticated.Header.Set("Authorization", "token "+t.token)
	authenticated.Header.Set("Accept", "application/json")
	return t.next.RoundTrip(authenticated)
}

// Writer sets tags on the application's repository.
type Writer struct {
	baseURL *url.URL
	owner   string
	repo    string
	http    *http.Client
}

// NewWriter builds a client able to set tags.
func NewWriter(options Options) (*Writer, error) {
	read, err := New(options)
	if err != nil {
		return nil, err
	}

	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	return &Writer{
		baseURL: read.baseURL,
		owner:   read.owner,
		repo:    read.repo,
		http: &http.Client{
			Timeout:   timeout,
			Transport: &writeTransport{next: http.DefaultTransport, token: options.Token},
		},
	}, nil
}

// CreateTag sets a lightweight tag on a commit.
//
// The commit is passed explicitly, never a branch: setting a tag on a branch would
// place it on whatever that branch names at that instant, and two releases created a
// minute apart could aim at different commits while believing they aim at the same
// point.
func (w *Writer) CreateTag(ctx context.Context, tag, commit, message string) error {
	if strings.TrimSpace(tag) == "" || strings.TrimSpace(commit) == "" {
		return fmt.Errorf("gitea: tag and commit are required to set a tag")
	}

	payload, err := json.Marshal(map[string]string{
		"tag_name": tag,
		"target":   commit,
		"message":  message,
	})
	if err != nil {
		return fmt.Errorf("gitea: encoding the tag request: %w", err)
	}

	target := w.baseURL.JoinPath("/api/v1/repos/" + w.owner + "/" + w.repo + "/tags")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("gitea: tag request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := w.http.Do(request)
	if err != nil {
		return fmt.Errorf("gitea: setting tag %q: %w", tag, err)
	}
	defer response.Body.Close()

	switch {
	case response.StatusCode == http.StatusCreated:
		return nil
	case response.StatusCode == http.StatusConflict,
		response.StatusCode == http.StatusUnprocessableEntity:
		// The forge returns 409 or 422 depending on its version when the tag exists. Both say
		// the same thing, and it is not a failure of the release.
		return fmt.Errorf("%w: %s", ErrTagExists, tag)
	case response.StatusCode == http.StatusForbidden,
		response.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("gitea: setting the tag refused (%d) — does the token have the write:repository scope?",
			response.StatusCode)
	default:
		return fmt.Errorf("gitea: setting tag %q: the forge answered %d", tag, response.StatusCode)
	}
}

// SetCommitStatus sets a status on a commit.
//
// The `context` is the replacement key: the forge displays one status per context per
// commit, and reusing the same name overwrites the previous one. That is what lets a
// chain publish `pending` at the start and then conclude it, rather than stacking
// contradictory states on the same pull request.
//
// The commit is passed resolved. A branch would be accepted by the forge and would
// place the status on whatever it names at that instant — hence, a minute later,
// elsewhere.
func (w *Writer) SetCommitStatus(ctx context.Context, commit, state, context_, description, targetURL string) error {
	commit = strings.TrimSpace(commit)
	if commit == "" || strings.TrimSpace(state) == "" || strings.TrimSpace(context_) == "" {
		return fmt.Errorf("gitea: commit, state and context are required to set a status")
	}

	return w.add(ctx, "/api/v1/repos/"+w.owner+"/"+w.repo+"/statuses/"+commit,
		map[string]string{
			"state":       state,
			"context":     context_,
			"description": description,
			"target_url":  targetURL,
		},
		fmt.Sprintf("setting status %q on %s", context_, shortCommit(commit)))
}

// CommentOnPullRequest adds a comment to a pull request.
//
// It goes through the issues API rather than the pull requests API, and that is not a
// workaround: in this forge a pull request is an augmented issue, and its discussion
// thread lives under that route. The pull requests route carries only reviews.
func (w *Writer) CommentOnPullRequest(ctx context.Context, number int, body string) error {
	if number <= 0 {
		return fmt.Errorf("gitea: invalid pull request number: %d", number)
	}
	if strings.TrimSpace(body) == "" {
		// Refused rather than sent: an empty comment posts without error and leaves a silent
		// trace in a team's thread, which has to be removed by hand.
		return fmt.Errorf("gitea: an empty comment is not posted")
	}

	return w.add(ctx,
		fmt.Sprintf("/api/v1/repos/%s/%s/issues/%d/comments", w.owner, w.repo, number),
		map[string]string{"body": body},
		fmt.Sprintf("comment on pull request #%d", number))
}

// add sends a JSON POST and judges the response.
//
// The action describes what was being attempted, and composes the error: "the forge
// answered 403" without saying to what forces someone to find the call in the log to
// understand what was refused.
func (w *Writer) add(ctx context.Context, path string, payload map[string]string, geste string) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("gitea: encoding the request (%s): %w", geste, err)
	}

	target := w.baseURL.JoinPath(path)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("gitea: request (%s): %w", geste, err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := w.http.Do(request)
	if err != nil {
		return fmt.Errorf("gitea: %s: %w", geste, err)
	}
	defer response.Body.Close()

	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return nil
	case response.StatusCode == http.StatusForbidden,
		response.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("gitea: %s refused (%d) — does the token have the write:repository scope?",
			geste, response.StatusCode)
	case response.StatusCode == http.StatusNotFound:
		// Told apart from a refusal: on these routes the forge returns 404 both when the
		// token cannot see the repository and when the target does not exist. Saying so
		// avoids hunting for a commit that is right there.
		return fmt.Errorf("gitea: %s: not found (404) — the target or the repository "+
			"is out of the token's reach", geste)
	default:
		return fmt.Errorf("gitea: %s: the forge answered %d", geste, response.StatusCode)
	}
}

// shortCommit abbreviates a SHA for a message.
func shortCommit(commit string) string {
	if len(commit) <= 8 {
		return commit
	}
	return commit[:8]
}

// ResolveCommit returns the SHA a reference points at.
//
// A release must name a commit and not a branch: a branch moves, and rebuilding the
// same release six months later would then produce a different image. It is the first
// link of the version invariant.
func (c *Client) ResolveCommit(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("gitea: empty reference")
	}

	query := url.Values{}
	query.Set("sha", ref)
	query.Set("limit", "1")
	// Commit bodies are useless here and costly: `stat=false` avoids fetching the detail
	// of changed files only to read the SHA.
	query.Set("stat", "false")
	query.Set("verification", "false")
	query.Set("files", "false")

	body, err := c.get(ctx, "/repos/"+c.owner+"/"+c.repo+"/commits", query)
	if err != nil {
		return "", err
	}

	var commits []struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(body, &commits); err != nil {
		return "", fmt.Errorf("gitea: resolution response unreadable: %w", err)
	}
	if len(commits) == 0 || commits[0].SHA == "" {
		return "", fmt.Errorf("gitea: reference %q names no commit", ref)
	}
	return commits[0].SHA, nil
}
