package gitea

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

/*
 * Accès à la forge Gitea, en lecture et rien d'autre.
 *
 * Même contrat que `internal/redmine`, pour la même raison : l'interdiction
 * d'écrire n'est pas confiée à la discipline des appelants mais au transport, qui
 * refuse toute méthode autre que GET et HEAD avant contact réseau. Le jour où le
 * dashboard devra écrire sur la forge — poser un tag, publier une release Gitea —
 * ce sera un client distinct et une décision explicite, pas un assouplissement de
 * celui-ci.
 */

// ErrWriteForbidden signale une tentative d'écriture sur la forge.
var ErrWriteForbidden = errors.New("gitea: écriture interdite, cet accès est en lecture seule")

// ErrNotConfigured signale l'absence d'URL, de jeton ou de dépôt.
var ErrNotConfigured = errors.New("gitea: accès non configuré")

var readMethods = map[string]bool{
	http.MethodGet:  true,
	http.MethodHead: true,
}

// readOnlyTransport interdit l'écriture et porte l'authentification.
//
// Le jeton n'est ajouté qu'ici : aucun appelant n'a l'occasion de le placer dans
// une URL, où il finirait dans les journaux d'accès et les messages d'erreur.
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

// Client interroge la forge en lecture.
type Client struct {
	baseURL *url.URL
	// owner et repo sont fixés à la construction : ce client lit le dépôt de
	// l'application, pas la forge entière.
	owner string
	repo  string
	http  *http.Client
}

// Options porte la configuration d'un client.
type Options struct {
	// URL de la forge, par exemple `https://forge.cadoles.com`.
	URL string
	// Token est un jeton d'API Gitea. Il ne doit apparaître ni dans un log, ni
	// dans un message d'erreur, ni dans une URL.
	Token string
	// Repository au format `owner/nom`, par exemple `CNOUS/mse`.
	Repository string
	Timeout    time.Duration
}

// New construit un client en lecture seule.
func New(options Options) (*Client, error) {
	if strings.TrimSpace(options.URL) == "" || strings.TrimSpace(options.Token) == "" ||
		strings.TrimSpace(options.Repository) == "" {
		return nil, ErrNotConfigured
	}

	owner, repo, found := strings.Cut(strings.TrimSpace(options.Repository), "/")
	if !found || owner == "" || repo == "" {
		return nil, fmt.Errorf("gitea: dépôt %q invalide, attendu owner/nom", options.Repository)
	}

	base, err := url.Parse(strings.TrimRight(options.URL, "/"))
	if err != nil {
		return nil, fmt.Errorf("gitea: URL invalide: %w", err)
	}
	if base.Scheme != "https" && base.Hostname() != "localhost" && base.Hostname() != "127.0.0.1" {
		// Le jeton voyage à chaque requête, et il ouvre la forge entière au nom de
		// son détenteur — pas seulement ce dépôt.
		return nil, fmt.Errorf("gitea: %s exige https", base.Redacted())
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

// get lit une ressource de l'API. Seul point de sortie réseau du paquet.
func (c *Client) get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	target := c.baseURL.JoinPath("/api/v1" + path)
	if len(query) > 0 {
		target.RawQuery = query.Encode()
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("gitea: requête: %w", err)
	}

	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("gitea: appel à %s: %w", target.Redacted(), err)
	}
	defer response.Body.Close()

	switch {
	case response.StatusCode == http.StatusUnauthorized, response.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("gitea: accès refusé sur %s (%d) — jeton invalide ou sans droit sur le dépôt",
			target.Path, response.StatusCode)
	case response.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("gitea: %s introuvable — le dépôt %s/%s existe-t-il ?",
			target.Path, c.owner, c.repo)
	case response.StatusCode >= 300:
		return nil, fmt.Errorf("gitea: %s a répondu %d", target.Path, response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("gitea: lecture de la réponse de %s: %w", target.Path, err)
	}
	return body, nil
}

// pageSize est la taille de page demandée.
//
// Cinquante est le défaut du plafond configurable de Gitea (`MAX_RESPONSE_ITEMS`) :
// demander davantage serait silencieusement tronqué à ce plafond, et la pagination
// par « page courte » croirait la liste finie.
const pageSize = 50

// maxRefs borne le nombre total de références lues.
//
// Le formulaire propose une liste ; personne ne choisit parmi des milliers
// d'entrées. La borne protège d'un dépôt aux branches jamais nettoyées, et son
// franchissement est signalé par l'appelant plutôt que tronqué en silence.
const maxRefs = 300

// Ref est une branche ou un tag.
type Ref struct {
	Name string
	// SHA du commit visé. Informatif : le champ `git_ref` d'une release porte le
	// nom, la résolution en commit appartient au pipeline au moment du build.
	SHA string
}

// DefaultBranch rend la branche par défaut du dépôt.
//
// C'est la valeur proposée d'office par le formulaire : le cas courant est de
// construire depuis la branche principale, et la demander à la forge évite de
// supposer `main` là où l'existant est sur `develop`.
func (c *Client) DefaultBranch(ctx context.Context) (string, error) {
	body, err := c.get(ctx, "/repos/"+c.owner+"/"+c.repo, nil)
	if err != nil {
		return "", err
	}

	var payload struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("gitea: dépôt illisible: %w", err)
	}
	return payload.DefaultBranch, nil
}

// ListBranches rend les branches du dépôt, toutes pages réunies.
func (c *Client) ListBranches(ctx context.Context) ([]Ref, bool, error) {
	return c.listRefs(ctx, "/branches", func(body []byte) ([]Ref, error) {
		var payload []struct {
			Name   string `json:"name"`
			Commit struct {
				ID string `json:"id"`
			} `json:"commit"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("gitea: liste des branches illisible: %w", err)
		}
		refs := make([]Ref, 0, len(payload))
		for _, raw := range payload {
			refs = append(refs, Ref{Name: raw.Name, SHA: raw.Commit.ID})
		}
		return refs, nil
	})
}

// ListTags rend les tags du dépôt, du plus récent au plus ancien.
func (c *Client) ListTags(ctx context.Context) ([]Ref, bool, error) {
	return c.listRefs(ctx, "/tags", func(body []byte) ([]Ref, error) {
		var payload []struct {
			Name   string `json:"name"`
			Commit struct {
				SHA string `json:"sha"`
			} `json:"commit"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("gitea: liste des tags illisible: %w", err)
		}
		refs := make([]Ref, 0, len(payload))
		for _, raw := range payload {
			refs = append(refs, Ref{Name: raw.Name, SHA: raw.Commit.SHA})
		}
		return refs, nil
	})
}

// listRefs pagine jusqu'à la page courte, la borne, ou la page vide.
//
// Le second retour dit si la liste a été tronquée par la borne : l'appelant le
// signale, une liste silencieusement incomplète se lisant comme exhaustive.
func (c *Client) listRefs(ctx context.Context, path string, decode func([]byte) ([]Ref, error)) ([]Ref, bool, error) {
	var refs []Ref
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

// Account est l'identité derrière le jeton.
type Account struct {
	Login string `json:"login"`
	Admin bool   `json:"is_admin"`
}

// CheckAccess vérifie le jeton et rend le compte auquel il appartient.
//
// Même rôle que pour Redmine : savoir qu'un jeton fonctionne sans attendre qu'un
// formulaire s'en serve, et voir s'il appartient à un compte administrateur — un
// jeton trop puissant reste trop puissant même inutilisé.
func (c *Client) CheckAccess(ctx context.Context) (Account, error) {
	body, err := c.get(ctx, "/user", nil)
	if err != nil {
		return Account{}, err
	}

	var account Account
	if err := json.Unmarshal(body, &account); err != nil {
		return Account{}, fmt.Errorf("gitea: réponse de /user illisible: %w", err)
	}
	return account, nil
}
