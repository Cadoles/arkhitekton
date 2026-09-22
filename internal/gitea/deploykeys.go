package gitea

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

/*
 * The deploy keys declared on a repository.
 *
 * Read to answer a single question: is the key the dashboard produced for this project set there?
 * It is the act the dashboard cannot perform itself — pasting the public half into a repository's
 * settings belongs to a human — and **observing** it is what avoids having them confirm it by
 * hand.
 *
 * The distinction applied to another object: a human confirmation is an **intention**, reading
 * the forge is an **observation**. The first could stay true after the key had been removed from
 * the repository, and the pipelines then failed on an SSH error that nothing connected to the
 * screen asserting the opposite.
 */

// deployKey is what the forge returns for a repository key.
type deployKey struct {
	// Key carries the type, the material and sometimes a comment, separated by spaces:
	// `ssh-ed25519 AAAAC3Nz… arkhitekton`.
	Key   string `json:"key"`
	Title string `json:"title"`
}

// DeployKeys returns the deploy keys declared on the client's repository.
//
// The token must have administration rights on the repository: the forge reserves
// this endpoint, and a read-only token receives 403. The caller must treat the
// failure as "I do not know" and not as "the key is absent" — the two do not call for
// the same response.
func (c *Client) DeployKeys(ctx context.Context) ([]string, error) {
	var keys []string

	// Paged like the references: a repository may carry more keys than one page, and
	// stopping at the first would conclude "absent" about a key that comes later.
	for page := 1; len(keys) < maxDeployKeys; page++ {
		query := url.Values{
			"page":  {strconv.Itoa(page)},
			"limit": {strconv.Itoa(pageSize)},
		}
		body, err := c.get(ctx, "/repos/"+c.owner+"/"+c.repo+"/keys", query)
		if err != nil {
			return nil, err
		}

		var batch []deployKey
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, fmt.Errorf("gitea: deploy keys unreadable: %w", err)
		}
		for _, entry := range batch {
			keys = append(keys, entry.Key)
		}
		if len(batch) < pageSize {
			break
		}
	}
	return keys, nil
}

// maxDeployKeys bounds the read. A repository carrying more has a different problem
// from the one being diagnosed here.
const maxDeployKeys = 200

// SameKey says whether two SSH public keys name the same pair.
//
// The comparison is on the type and the material, never on the comment: that is free
// text, often rewritten by the forge's interface when pasted, and comparing whole
// strings would answer "absent" about a key that is in place. Exactly the kind of
// false negative that would send someone to paste a key already installed.
func SameKey(a, b string) bool {
	typeA, matiereA, okA := splitKey(a)
	typeB, matiereB, okB := splitKey(b)
	return okA && okB && typeA == typeB && matiereA == matiereB
}

// splitKey isolates the type and the material of a public key.
func splitKey(key string) (keyType, material string, ok bool) {
	fields := strings.Fields(strings.TrimSpace(key))
	if len(fields) < 2 {
		return "", "", false
	}
	return fields[0], fields[1], true
}
