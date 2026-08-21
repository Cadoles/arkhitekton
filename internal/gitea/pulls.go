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
 * Pull requests fusionnées, pour la note de release.
 *
 * Seules les fusionnées comptent : une PR ouverte ou refusée n'a rien livré, et
 * la faire figurer dans une note annoncerait au client une correction qui n'est
 * pas dans l'image.
 */

// maxPulls borne le nombre de PR examinées.
//
// La note d'une release couvre ce qui a été fusionné depuis la précédente —
// quelques dizaines de PR sur un cycle. Deux cents laisse une marge confortable
// tout en bornant le coût : chaque PR retenue coûte une seconde requête pour ses
// commits, et remonter l'historique entier du dépôt en coûterait des milliers
// pour des demandes livrées il y a deux ans.
const maxPulls = 200

// PullRequest est une PR fusionnée, réduite à ce qui sert au rapprochement.
type PullRequest struct {
	Number   int64
	Title    string
	Body     string
	MergedAt *time.Time
	// Commits porte les messages des commits de la PR. Renseigné par
	// `LoadCommitMessages` : la liste des PR ne les contient pas.
	Commits []string
}

// Reference rend `#42`, tel qu'on l'écrit dans une note.
func (p PullRequest) Reference() string {
	return "#" + strconv.FormatInt(p.Number, 10)
}

// ListMergedPullRequests rend les PR fusionnées, de la plus récente à la plus
// ancienne.
//
// `since` borne la lecture : les PR fusionnées avant sont ignorées. Passer le
// zéro lit jusqu'à `maxPulls`. La borne est appliquée ici plutôt que par l'API —
// Gitea ne sait pas filtrer les PR sur leur date de fusion — d'où une lecture
// qui s'arrête dès qu'elle a dépassé la date.
func (c *Client) ListMergedPullRequests(ctx context.Context, since time.Time) ([]PullRequest, error) {
	var pulls []PullRequest

	for page := 1; len(pulls) < maxPulls; page++ {
		query := url.Values{}
		query.Set("state", "closed")
		// Gitea trie par date de mise à jour décroissante : les fusions récentes
		// arrivent en premier, ce qui permet de s'arrêter tôt.
		query.Set("sort", "recentupdate")
		query.Set("limit", strconv.Itoa(pageSize))
		query.Set("page", strconv.Itoa(page))

		body, err := c.get(ctx, "/repos/"+c.owner+"/"+c.repo+"/pulls", query)
		if err != nil {
			return nil, err
		}

		var decoded []struct {
			Number   int64      `json:"number"`
			Title    string     `json:"title"`
			Body     string     `json:"body"`
			Merged   bool       `json:"merged"`
			MergedAt *time.Time `json:"merged_at"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, fmt.Errorf("gitea: réponse des pull requests illisible: %w", err)
		}
		if len(decoded) == 0 {
			break
		}

		for _, item := range decoded {
			// Fermée sans fusion : la demande n'a rien livré.
			if !item.Merged {
				continue
			}
			if !since.IsZero() && item.MergedAt != nil && !item.MergedAt.After(since) {
				continue
			}
			pulls = append(pulls, PullRequest{
				Number: item.Number, Title: item.Title, Body: item.Body,
				MergedAt: item.MergedAt,
			})
			if len(pulls) >= maxPulls {
				break
			}
		}

		if len(decoded) < pageSize {
			break
		}
	}
	return pulls, nil
}

// LoadCommitMessages complète une PR de ses messages de commit.
//
// Séparé de la liste parce que Gitea ne les rend pas avec elle, et que la
// convention autorise le numéro de demande dans le titre **ou** dans un commit :
// s'en passer manquerait les PR dont le titre ne cite rien.
//
// Un échec est rendu tel quel plutôt qu'ignoré : une PR dont les commits n'ont
// pas pu être lus peut cacher une demande, et le silence ferait passer une note
// incomplète pour une note exhaustive.
func (c *Client) LoadCommitMessages(ctx context.Context, pull *PullRequest) error {
	query := url.Values{}
	query.Set("limit", strconv.Itoa(pageSize))

	body, err := c.get(ctx,
		fmt.Sprintf("/repos/%s/%s/pulls/%d/commits", c.owner, c.repo, pull.Number), query)
	if err != nil {
		return err
	}

	var decoded []struct {
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return fmt.Errorf("gitea: commits de la PR %d illisibles: %w", pull.Number, err)
	}

	pull.Commits = make([]string, 0, len(decoded))
	for _, item := range decoded {
		pull.Commits = append(pull.Commits, item.Commit.Message)
	}
	return nil
}
