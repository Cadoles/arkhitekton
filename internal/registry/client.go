// Package registry lit le registre d'images par l'API OCI distribution.
//
// Il ne sert qu'à **constater**. Le dashboard ne pousse jamais d'image : le
// re-tag de promotion est fait par la Task `promote-image`, avec ses propres
// identifiants. Le compte employé ici n'a donc besoin que de la lecture, et lui
// donner l'écriture ferait porter au dashboard un pouvoir qu'il n'exerce pas.
//
// Ce que le registre apporte que rien d'autre ne donne : **la liste des tags
// portés par un digest**. Une promotion étant un re-tag du même digest (B7), cette
// liste dit à la fois quelle version est une image et où elle a été promue — y
// compris pour la production, dont le cluster est hors de portée (B8).
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

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/config"
)

// Types de manifeste demandés.
//
// Les deux formes coexistent dans un registre réel : les images anciennes sont en
// manifeste v2 simple, les récentes en liste multi-architecture. Ne demander que
// la première ferait échouer la résolution de digest sur les secondes, avec un
// 404 qui laisse croire à une image absente.
var acceptManifests = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// Client interroge un registre.
type Client struct {
	base     *url.URL
	username string
	password string
	http     *http.Client
	// project est le préfixe des dépôts, sans l'hôte.
	project string
}

// ErrNotConfigured signale une absence d'identifiants.
//
// Distinct d'une erreur d'accès : le dashboard doit pouvoir tourner sans registre
// — l'inventaire des images manque alors, le reste fonctionne — et l'interface
// doit pouvoir le dire au lieu d'afficher une panne.
var ErrNotConfigured = errors.New("registre non configuré")

// Open construit le client. Rend ErrNotConfigured si les identifiants manquent.
func Open(cfg config.RegistryConfig) (*Client, error) {
	if strings.TrimSpace(cfg.Username) == "" || strings.TrimSpace(cfg.Password) == "" {
		return nil, ErrNotConfigured
	}

	base, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("URL de registre invalide %q: %w", cfg.URL, err)
	}

	// `registry.project` porte l'hôte (`reg.cadoles.com/cnous-mse`) parce que
	// c'est sous cette forme qu'il entre dans les paramètres de pipeline. L'API
	// du registre, elle, n'attend que le chemin.
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

// Project rend le préfixe des dépôts, sans l'hôte.
func (c *Client) Project() string { return c.project }

// Tags liste les tags d'un dépôt, dans l'ordre rendu par le registre.
func (c *Client) Tags(ctx context.Context, repository string) ([]string, error) {
	var payload struct {
		Tags []string `json:"tags"`
	}

	if err := c.getJSON(ctx, fmt.Sprintf("/v2/%s/tags/list", c.path(repository)), &payload); err != nil {
		return nil, err
	}
	return payload.Tags, nil
}

// Digest rend le digest d'un tag.
//
// Il est lu dans l'en-tête `Docker-Content-Digest` d'une requête HEAD : demander
// le manifeste entier pour n'en garder que l'empreinte ferait télécharger des
// kilo-octets par tag, et un dépôt en compte des centaines.
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
		return "", fmt.Errorf("digest de %s:%s: statut %d", repository, tag, response.StatusCode)
	}

	digest := response.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("digest de %s:%s: en-tête absent", repository, tag)
	}
	return digest, nil
}

// TagsByDigest indexe les tags d'un dépôt par digest.
//
// C'est l'opération qui donne son intérêt au registre : une promotion étant un
// re-tag du même digest, les tags d'un digest disent à la fois quelle version
// c'est et où elle a été promue. Sans elle, il faudrait interroger chaque
// environnement — impossible pour la production.
//
// Le coût est d'une requête HEAD par tag. `limit` borne le balayage aux tags les
// plus récents rendus par le registre : un dépôt de plusieurs centaines de tags
// prendrait sinon des minutes, pour une information qui ne concerne que les
// versions vivantes.
func (c *Client) TagsByDigest(ctx context.Context, repository string, limit int) (map[string][]string, error) {
	tags, err := c.Tags(ctx, repository)
	if err != nil {
		return nil, err
	}

	if limit > 0 && len(tags) > limit {
		// Les registres rendent les tags par ordre alphabétique croissant : la
		// fin de la liste est la plus récente pour un schéma daté comme pour du
		// SemVer.
		tags = tags[len(tags)-limit:]
	}

	byDigest := make(map[string][]string, len(tags))
	for _, tag := range tags {
		if ctx.Err() != nil {
			return byDigest, ctx.Err()
		}

		digest, err := c.Digest(ctx, repository, tag)
		if err != nil {
			// Un tag illisible — supprimé entre le listing et la résolution, ou
			// manifeste d'un type inattendu — ne doit pas faire perdre les
			// autres.
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
		return nil, fmt.Errorf("requête vers %s: %w", path, err)
	}
	return request, nil
}

// do exécute la requête en gérant l'authentification.
//
// Deux schémas coexistent selon les registres : `Basic` accepté directement, ou
// un jeton porteur à obtenir auprès d'un service d'autorisation désigné par
// l'en-tête `WWW-Authenticate`. Ne traiter que le premier fonctionne sur
// `reg.cadoles.com` aujourd'hui et échouerait sur un registre qui change de
// configuration — pour un coût de vingt lignes.
func (c *Client) do(request *http.Request) (*http.Response, error) {
	request.SetBasicAuth(c.username, c.password)

	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("appel au registre %s: %w", c.base.Host, err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		return response, nil
	}

	challenge := response.Header.Get("WWW-Authenticate")
	_ = response.Body.Close()

	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		// Le registre refuse en Basic : l'identifiant est en cause, pas le
		// schéma. Rendre la réponse telle quelle laisse l'appelant produire un
		// message qui le dit.
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
		return nil, fmt.Errorf("appel au registre %s: %w", c.base.Host, err)
	}
	return response, nil
}

func (c *Client) retry(request *http.Request) (*http.Response, error) {
	retried := request.Clone(request.Context())
	retried.SetBasicAuth(c.username, c.password)

	response, err := c.http.Do(retried)
	if err != nil {
		return nil, fmt.Errorf("appel au registre %s: %w", c.base.Host, err)
	}
	return response, nil
}

// token obtient un jeton porteur auprès du service désigné par le défi.
func (c *Client) token(ctx context.Context, challenge string) (string, error) {
	parameters := parseChallenge(challenge)

	realm := parameters["realm"]
	if realm == "" {
		return "", fmt.Errorf("défi d'authentification sans realm: %q", challenge)
	}

	target, err := url.Parse(realm)
	if err != nil {
		return "", fmt.Errorf("realm invalide %q: %w", realm, err)
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
		return "", fmt.Errorf("demande de jeton: %w", err)
	}
	request.SetBasicAuth(c.username, c.password)

	response, err := c.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("demande de jeton: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("demande de jeton refusée: statut %d", response.StatusCode)
	}

	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return "", fmt.Errorf("jeton illisible: %w", err)
	}

	// Les deux noms circulent : `token` dans la spécification Docker,
	// `access_token` dans les implémentations alignées sur OAuth2.
	if payload.Token != "" {
		return payload.Token, nil
	}
	if payload.AccessToken != "" {
		return payload.AccessToken, nil
	}
	return "", errors.New("le service d'autorisation n'a rendu aucun jeton")
}

// parseChallenge découpe un en-tête `WWW-Authenticate` de type Bearer.
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
		return fmt.Errorf("%s: statut %d", path, response.StatusCode)
	}

	// Borne de lecture : un registre compromis ou en panne ne doit pas pouvoir
	// faire enfler la mémoire du worker.
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(target); err != nil {
		return fmt.Errorf("%s: réponse illisible: %w", path, err)
	}
	return nil
}
