package gitea

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/provider"
)

/*
 * Test d'accès à un dépôt qu'on vient de déclarer.
 *
 * Distinct du Client, et pas par commodité : un Client est lié à un dépôt à sa
 * construction, alors que le test porte sur un dépôt dont on ne sait pas encore
 * s'il est joignable. D'où un client par appel.
 *
 * Vivait dans `internal/http`, où il était le seul appel à une API extérieure du
 * paquet : la couche HTTP traduisait un échec en constat lisible **et** parlait à
 * Gitea. Le second rôle appartient ici, avec les autres lectures de la forge
 * (doc 12 §12.9, chantier 4).
 */

// Prober teste un dépôt par l'API Gitea.
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
