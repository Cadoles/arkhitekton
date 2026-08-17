package redmine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

/*
 * Lecture des versions et des demandes.
 *
 * La synchronisation est une interrogation périodique incrémentale, et c'est la
 * voie définitive et non un repli : aucun plugin webhook n'est déployable sur
 * l'instance ([B6](../../docs/02-blocages-et-decisions.md#b6)).
 */

// Version est une version déclarée dans Redmine.
//
// `Name` est conservé tel quel. L'instance réelle en porte quatre conventions
// simultanées — `MSE3-1.5`, `MSE 3.1.0`, `v3.2.0` — et normaliser à l'écriture
// perdrait l'information sans rien résoudre. L'identité est `ID`, jamais le nom.
type Version struct {
	ID          int64
	Name        string
	Description string
	// Status vaut `open`, `locked` ou `closed`.
	Status    string
	DueDate   *time.Time
	UpdatedOn *time.Time
}

// Issue est une demande, réduite à ce que le dashboard affiche.
//
// La description n'est pas reprise : elle porte le détail métier, parfois
// nominatif, et le dashboard renvoie vers Redmine pour la consulter. Ne pas
// recopier ce dont on n'a pas besoin est la façon la plus simple de ne pas avoir à
// le protéger.
type Issue struct {
	ID        int64
	VersionID *int64
	Tracker   string
	Subject   string
	Status    string
	// StatusClosed est le drapeau de Redmine, repris sans jugement. La projection
	// en `todo`/`doing`/`done`/`rejected` est une décision du dashboard, portée par
	// une table éditable, et les deux peuvent diverger : l'instance marque
	// `Ré-ouvert` comme clos, ce qui ressemble à une erreur de configuration.
	StatusClosed bool
	Priority     string
	Assignee     string
	DoneRatio    int
	UpdatedOn    *time.Time
}

// redminePageSize est la taille de page demandée à Redmine.
//
// Cent est le plafond de l'API : demander davantage est silencieusement ramené à
// vingt-cinq, ce qui multiplierait le nombre d'allers-retours par quatre sans
// aucun message.
const redminePageSize = 100

// ListVersions rend les versions d'un projet.
//
// Non paginé du côté de Redmine, qui rend toutes les versions d'un projet en une
// réponse. La borne de lecture du corps protège du cas où ce ne serait plus vrai.
func (c *Client) ListVersions(ctx context.Context, project string) ([]Version, error) {
	if project == "" {
		return nil, fmt.Errorf("redmine: projet non renseigné")
	}

	body, err := c.get(ctx, "/projects/"+url.PathEscape(project)+"/versions.json", nil)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Versions []struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Status      string `json:"status"`
			DueDate     string `json:"due_date"`
			UpdatedOn   string `json:"updated_on"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("redmine: liste des versions illisible: %w", err)
	}

	versions := make([]Version, 0, len(payload.Versions))
	for _, raw := range payload.Versions {
		versions = append(versions, Version{
			ID: raw.ID, Name: raw.Name, Description: raw.Description,
			Status:    raw.Status,
			DueDate:   parseDate(raw.DueDate),
			UpdatedOn: parseTime(raw.UpdatedOn),
		})
	}
	return versions, nil
}

// IssueQuery borne une lecture de demandes.
type IssueQuery struct {
	// Project est l'identifiant ou le raccourci du projet.
	Project string
	// Since restreint aux demandes modifiées depuis cet instant. Zéro pour tout
	// lire, ce qui n'arrive qu'à la première synchronisation.
	Since time.Time
}

// ListIssues rend les demandes correspondant à la requête, toutes pages réunies.
//
// `status_id=*` est indispensable : sans lui, Redmine ne rend que les demandes
// ouvertes, et une version terminée paraîtrait vide. C'est le genre de défaut
// silencieux qui se lit comme une perte de données.
//
// Le tri est imposé sur `updated_on` croissant. Sans ordre stable, une demande
// modifiée pendant la pagination peut changer de page et échapper à la lecture —
// et c'est précisément la demande qui vient de bouger qu'on cherche.
func (c *Client) ListIssues(ctx context.Context, query IssueQuery) ([]Issue, error) {
	if query.Project == "" {
		return nil, fmt.Errorf("redmine: projet non renseigné")
	}

	var issues []Issue
	for offset := 0; ; {
		params := url.Values{}
		params.Set("project_id", query.Project)
		params.Set("status_id", "*")
		params.Set("sort", "updated_on")
		params.Set("limit", strconv.Itoa(redminePageSize))
		params.Set("offset", strconv.Itoa(offset))
		if !query.Since.IsZero() {
			// Redmine attend un opérateur dans la valeur ; `url.Values` encode le
			// `>=` correctement.
			params.Set("updated_on", ">="+query.Since.UTC().Format(time.RFC3339))
		}

		body, err := c.get(ctx, "/issues.json", params)
		if err != nil {
			return nil, err
		}

		page, total, err := decodeIssues(body)
		if err != nil {
			return nil, err
		}
		issues = append(issues, page...)

		// Arrêt sur page vide autant que sur total atteint : une page vide alors que
		// le total annonce davantage signalerait une incohérence côté Redmine, et
		// boucler dessus tournerait sans fin.
		offset += len(page)
		if len(page) == 0 || offset >= total {
			return issues, nil
		}
	}
}

func decodeIssues(body []byte) ([]Issue, int, error) {
	var payload struct {
		TotalCount int `json:"total_count"`
		Issues     []struct {
			ID      int64  `json:"id"`
			Subject string `json:"subject"`
			Tracker struct {
				Name string `json:"name"`
			} `json:"tracker"`
			Status struct {
				Name     string `json:"name"`
				IsClosed bool   `json:"is_closed"`
			} `json:"status"`
			Priority struct {
				Name string `json:"name"`
			} `json:"priority"`
			AssignedTo struct {
				Name string `json:"name"`
			} `json:"assigned_to"`
			FixedVersion *struct {
				ID int64 `json:"id"`
			} `json:"fixed_version"`
			DoneRatio int    `json:"done_ratio"`
			UpdatedOn string `json:"updated_on"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, 0, fmt.Errorf("redmine: liste des demandes illisible: %w", err)
	}

	issues := make([]Issue, 0, len(payload.Issues))
	for _, raw := range payload.Issues {
		issue := Issue{
			ID: raw.ID, Subject: raw.Subject,
			Tracker:      raw.Tracker.Name,
			Status:       raw.Status.Name,
			StatusClosed: raw.Status.IsClosed,
			Priority:     raw.Priority.Name,
			Assignee:     raw.AssignedTo.Name,
			DoneRatio:    raw.DoneRatio,
			UpdatedOn:    parseTime(raw.UpdatedOn),
		}
		// Une demande sans version est le cas courant : le rattachement à une
		// version est un geste de planification qui n'a pas toujours eu lieu.
		if raw.FixedVersion != nil {
			id := raw.FixedVersion.ID
			issue.VersionID = &id
		}
		issues = append(issues, issue)
	}
	return issues, payload.TotalCount, nil
}

func parseTime(raw string) *time.Time {
	if raw == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		// Un horodatage illisible ne doit pas faire échouer la synchronisation
		// entière : l'absence se traduit par une resynchronisation complète au
		// prochain passage, ce qui est coûteux mais correct.
		return nil
	}
	return &parsed
}

func parseDate(raw string) *time.Time {
	if raw == "" {
		return nil
	}
	parsed, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return nil
	}
	return &parsed
}
