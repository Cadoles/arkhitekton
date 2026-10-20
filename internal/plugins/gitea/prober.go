package gitea

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

/*
 * Testing access to a repository one has just declared.
 *
 * Distinct from the Client, and not out of convenience: a Client is bound to a repository at its
 * construction, whereas the test bears on a repository one does not yet know to be reachable.
 * Hence one client per call.
 *
 * It lived in `internal/http`, where it was the package's only call to an outside API: the HTTP
 * layer translated a failure into a readable observation **and** spoke to Gitea. The second role
 * belongs here, with the forge's other reads.
 */

// Prober tests a repository through the Gitea API.
type Prober struct {
	URL     string
	Token   string
	Timeout time.Duration
}

// Probe returns the repository's access verdict, or the error preventing it.
//
// Two questions in one call, deliberately: they are about the same repository and are
// asked at the same moment, when someone wants to know whether this project is ready
// to build. Separating them into two buttons would mean pressing two to get one
// answer.
func (p Prober) Probe(ctx context.Context, repository, publicKey string) (provider.Probe, error) {
	client, err := New(Options{
		URL: p.URL, Token: p.Token, Repository: repository, Timeout: p.Timeout,
	})
	if err != nil {
		return provider.Probe{}, fmt.Errorf("client: %w", err)
	}

	branch, err := client.DefaultBranch(ctx)
	if err != nil {
		return provider.Probe{}, err
	}
	if branch == "" {
		// A repository with no default branch exists but never received a commit: a
		// pipeline's clone would fail there, and saying so here avoids learning it mid-build.
		return provider.Probe{}, errors.New("the repository is empty: no default branch")
	}

	probe := provider.Probe{DefaultBranch: branch}
	probe.KeyPresent, probe.KeyNote = p.checkKey(ctx, client, publicKey)
	return probe, nil
}

// checkKey looks for the project's key among the repository's.
//
// It returns three states. A missing answer is never translated into "key absent":
// the token may lack the administration right the forge requires to list keys, and
// concluding absence would then cut off the builds of a project whose key is in
// place.
func (p Prober) checkKey(ctx context.Context, client *Client, publicKey string) (*bool, string) {
	if strings.TrimSpace(publicKey) == "" {
		return nil, "no key of its own has been set for this project yet"
	}

	keys, err := client.DeployKeys(ctx)
	if err != nil {
		// The original message is kept: it is what tells a token without rights from an
		// unreachable forge, and the two are fixed differently.
		return nil, "repository keys unreadable (does the token have the administration " +
			"right?): " + err.Error()
	}

	for _, candidate := range keys {
		if SameKey(candidate, publicKey) {
			present := true
			return &present, "the project's key is among the repository's deploy keys"
		}
	}

	absent := false
	return &absent, fmt.Sprintf(
		"the project's key is not among the repository's %d deploy key(s)",
		len(keys))
}
