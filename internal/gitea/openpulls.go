package gitea

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

/*
 * Les demandes de fusion **ouvertes**, pour la sonde.
 *
 * Distinctes de `ListMergedPullRequests`, qui sert la note de release et ne
 * regarde que ce qui a été fusionné. Ici on cherche l'inverse : ce qui vient de
 * s'ouvrir, pour qu'une réaction puisse s'y accrocher.
 *
 * La date retenue est celle de **création**, jamais celle de mise à jour : une
 * demande ouverte il y a un mois et commentée ce matin n'est pas un fait nouveau,
 * et la traiter comme tel renotifierait à chaque commentaire.
 */

// OpenPullRequest est une demande de fusion ouverte, réduite à ce qu'un fait
// porte.
type OpenPullRequest struct {
	Number       int64
	Title        string
	Author       string
	SourceBranch string
	TargetBranch string
	URL          string
	CreatedAt    time.Time
}

// maxOpenPulls borne la lecture.
//
// Un dépôt actif porte quelques dizaines de demandes ouvertes. Deux cents laisse
// une marge et borne le coût : la sonde tourne à chaque intervalle, pour chaque
// projet, et une lecture non bornée ferait payer l'historique entier à chaque
// passage.
const maxOpenPulls = 200

// ListOpenPullRequests rend les demandes ouvertes créées après `since`.
//
// `since` nul lit jusqu'à la borne : c'est la passe d'amorçage, dont la boucle
// n'émet rien — elle ne sert qu'à établir le curseur.
//
// Le tri de Gitea porte sur la date de **mise à jour**, pas de création : on ne
// peut donc pas s'arrêter au premier élément trop ancien, et la borne de pages
// est ce qui tient le coût.
func (c *Client) ListOpenPullRequests(ctx context.Context, since time.Time) ([]OpenPullRequest, error) {
	var pulls []OpenPullRequest

	for page := 1; len(pulls) < maxOpenPulls; page++ {
		query := url.Values{}
		query.Set("state", "open")
		query.Set("sort", "recentupdate")
		query.Set("page", strconv.Itoa(page))
		query.Set("limit", strconv.Itoa(pageSize))

		body, err := c.get(ctx, "/repos/"+c.owner+"/"+c.repo+"/pulls", query)
		if err != nil {
			return nil, err
		}

		var batch []struct {
			Number  int64  `json:"number"`
			Title   string `json:"title"`
			HTMLURL string `json:"html_url"`
			User    struct {
				Login string `json:"login"`
			} `json:"user"`
			Head struct {
				Ref string `json:"ref"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
			CreatedAt time.Time `json:"created_at"`
		}
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, fmt.Errorf("gitea: demandes de fusion illisibles: %w", err)
		}

		for _, entry := range batch {
			// La date de **création** décide, jamais celle de mise à jour : une
			// demande ouverte il y a un mois et commentée ce matin n'est pas un
			// fait nouveau.
			if !since.IsZero() && !entry.CreatedAt.After(since) {
				continue
			}
			pulls = append(pulls, OpenPullRequest{
				Number: entry.Number, Title: entry.Title, Author: entry.User.Login,
				SourceBranch: entry.Head.Ref, TargetBranch: entry.Base.Ref,
				URL: entry.HTMLURL, CreatedAt: entry.CreatedAt,
			})
		}

		if len(batch) < pageSize {
			break
		}
	}

	return pulls, nil
}
