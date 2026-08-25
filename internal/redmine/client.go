package redmine

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

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/provider"
)

/*
 * Accès à Redmine, en lecture et rien d'autre.
 *
 * L'instance visée est celle de **production** : elle porte les demandes réelles
 * du client, et le dashboard n'a aucune raison d'y écrire. Créer une version,
 * fermer une demande, poster une note — tout cela reste un geste humain fait dans
 * Redmine.
 *
 * Cette interdiction n'est pas confiée à la discipline des appelants. Elle est
 * portée par le transport : toute requête qui n'est pas une lecture est refusée
 * avant d'atteindre le réseau. Un futur appelant qui tenterait un POST obtiendrait
 * une erreur immédiate, en développement comme en production, au lieu de modifier
 * une base dont on ne dispose pas de sauvegarde.
 *
 * Le choix du transport plutôt que d'une revue de code est délibéré : une revue
 * peut manquer un appel, un `RoundTrip` ne peut pas être contourné sans
 * réécrire la construction du client.
 */

// ErrWriteForbidden signale une tentative d'écriture sur Redmine.
//
// Cette erreur n'est pas un cas d'exécution normal à traiter : c'est le signe
// qu'un appelant a été écrit contre le contrat de ce paquet. Elle porte la méthode
// et le chemin pour que la faute soit trouvable, sans jamais porter la clé.
var ErrWriteForbidden = errors.New("redmine: écriture interdite, cet accès est en lecture seule")

// ErrNotConfigured signale l'absence d'URL ou de clé d'API.
var ErrNotConfigured = errors.New("redmine: accès non configuré")

// readOnlyTransport interdit l'écriture et porte l'authentification.
//
// Les deux responsabilités sont réunies à dessein : la clé n'est ajoutée qu'ici,
// donc aucun appelant n'a l'occasion de la placer ailleurs — notamment pas dans
// une chaîne de requête, où elle finirait dans les journaux d'accès du serveur et
// dans l'`url.Error` de toute requête en échec.
type readOnlyTransport struct {
	next   http.RoundTripper
	apiKey string
}

// Méthodes de lecture. `HEAD` est admis parce qu'il ne modifie rien ; il ne sert
// pas aujourd'hui, mais l'exclure obligerait à revenir ici pour un test de
// joignabilité, ce qui est exactement le genre de retouche où la garde saute.
var readMethods = map[string]bool{
	http.MethodGet:  true,
	http.MethodHead: true,
}

func (t *readOnlyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if !readMethods[request.Method] {
		// Refus avant tout contact réseau. Le message nomme la méthode et le
		// chemin, jamais la requête complète : une chaîne de requête peut porter
		// des identifiants de demandes, et le principe reste de ne pas recopier
		// dans un journal ce qu'on n'a pas choisi d'y mettre.
		return nil, fmt.Errorf("%w (%s %s)", ErrWriteForbidden, request.Method, request.URL.Path)
	}

	// L'en-tête, et non `?key=` : une chaîne de requête est journalisée par le
	// serveur, conservée dans les caches intermédiaires et recopiée dans les
	// messages d'erreur du client Go.
	//
	// La requête est clonée : muter celle de l'appelant est interdit par le
	// contrat de RoundTripper, et laisserait la clé dans un objet qu'il détient.
	authenticated := request.Clone(request.Context())
	authenticated.Header.Set("X-Redmine-API-Key", t.apiKey)
	authenticated.Header.Set("Accept", "application/json")

	return t.next.RoundTrip(authenticated)
}

// Client réalise [provider.Tracker], et cette assertion n'est pas décorative :
// elle fait qu'une méthode retirée d'ici, ou une signature qui dérive de ce que
// la frontière annonce, casse à la compilation plutôt qu'au premier appel.
var _ provider.Tracker = (*Client)(nil)

// Client interroge Redmine en lecture.
type Client struct {
	baseURL *url.URL
	http    *http.Client
}

// Options porte la configuration d'un client.
type Options struct {
	// URL de l'instance, par exemple `https://redmine.example.org`.
	URL string
	// APIKey est la clé d'API d'un compte Redmine. Elle ne doit apparaître ni dans
	// un log, ni dans un message d'erreur, ni dans une URL.
	APIKey string
	// Timeout borne un appel. Redmine sous charge répond parfois en plusieurs
	// secondes ; sans borne, une lenteur de Redmine deviendrait une lenteur du
	// dashboard.
	Timeout time.Duration
}

// New construit un client en lecture seule.
//
// Rend [ErrNotConfigured] si l'URL ou la clé manquent, plutôt qu'un client qui
// échouerait au premier appel : l'absence de configuration est une décision de
// déploiement, et le dashboard doit pouvoir tourner sans Redmine.
func New(options Options) (*Client, error) {
	if strings.TrimSpace(options.URL) == "" || strings.TrimSpace(options.APIKey) == "" {
		return nil, ErrNotConfigured
	}

	base, err := url.Parse(strings.TrimRight(options.URL, "/"))
	if err != nil {
		return nil, fmt.Errorf("redmine: URL invalide: %w", err)
	}
	if base.Scheme != "https" && base.Hostname() != "localhost" && base.Hostname() != "127.0.0.1" {
		// La clé voyage à chaque requête. En clair sur le réseau, elle est lisible
		// par tout intermédiaire, et il s'agit ici d'un accès à la production.
		return nil, fmt.Errorf("redmine: %s exige https", base.Redacted())
	}

	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	return &Client{
		baseURL: base,
		http: &http.Client{
			Timeout:   timeout,
			Transport: &readOnlyTransport{next: http.DefaultTransport, apiKey: options.APIKey},
		},
	}, nil
}

// get lit une ressource et rend son corps.
//
// Seul point de sortie réseau du paquet : toute lecture passe par ici, ce qui
// donne un endroit unique pour le traitement des codes d'erreur, et un endroit
// unique à relire pour vérifier qu'aucune écriture n'est possible.
func (c *Client) get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	target := c.baseURL.JoinPath(path)
	if len(query) > 0 {
		target.RawQuery = query.Encode()
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("redmine: requête: %w", err)
	}

	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("redmine: appel à %s: %w", target.Redacted(), err)
	}
	defer response.Body.Close()

	switch {
	case response.StatusCode == http.StatusUnauthorized, response.StatusCode == http.StatusForbidden:
		// Distingué du reste : c'est presque toujours une clé absente, révoquée ou
		// sans droit sur le projet, et non une panne de Redmine.
		return nil, fmt.Errorf("redmine: accès refusé sur %s (%d) — clé invalide ou sans droit sur le projet",
			target.Path, response.StatusCode)
	case response.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("redmine: %s introuvable", target.Path)
	case response.StatusCode >= 300:
		return nil, fmt.Errorf("redmine: %s a répondu %d", target.Path, response.StatusCode)
	}

	// 8 Mio : une liste de demandes paginée tient largement dedans, et une réponse
	// inattendue ne doit pas pouvoir épuiser la mémoire du processus.
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("redmine: lecture de la réponse de %s: %w", target.Path, err)
	}
	return body, nil
}

// CheckAccess vérifie la clé et rend le compte auquel elle appartient.
//
// `/users/current.json` est la lecture canonique pour valider une clé d'API : elle
// ne coûte rien à Redmine et ne touche à rien. C'est ce qui permet de dire « la clé
// est bonne » sans avoir à tenter une opération réelle.
//
// Le champ `admin` est rendu parce qu'il mérite d'être vu : une clé d'administrateur
// donne bien plus que ce dont le dashboard a besoin. Le transport en lecture seule
// empêche d'en abuser depuis ici, mais une clé trop puissante reste une clé trop
// puissante — mieux vaut le savoir et demander un compte restreint.
func (c *Client) CheckAccess(ctx context.Context) (provider.Account, error) {
	body, err := c.get(ctx, "/users/current.json", nil)
	if err != nil {
		return provider.Account{}, err
	}

	// Structure de fil, distincte du type de frontière : les noms de champs sont
	// ceux de Redmine, et les exposer obligerait toute autre réalisation à les
	// reprendre.
	var payload struct {
		User struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
			Admin bool   `json:"admin"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return provider.Account{}, fmt.Errorf("redmine: réponse de /users/current.json illisible: %w", err)
	}
	return provider.Account{
		ID: payload.User.ID, Login: payload.User.Login, Admin: payload.User.Admin,
	}, nil
}
