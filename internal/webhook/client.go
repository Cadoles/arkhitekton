package webhook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// Options borne ce qu'un appel sortant peut atteindre et coûter.
type Options struct {
	// AllowPrivate autorise les cibles privées et la boucle locale.
	//
	// Faux par défaut, et c'est le bon défaut : une instance de production n'a
	// aucune raison d'appeler son propre réseau, et l'autorisation générale est
	// exactement ce par quoi une falsification passe. Un déploiement local la
	// déclare, en connaissance de cause.
	//
	// Elle ne rouvre **jamais** le lien-local : les métadonnées d'instance ne sont
	// pas une cible qu'un déploiement veut atteindre.
	AllowPrivate bool
	// AllowInsecure autorise `http://`.
	//
	// Faux par défaut : une charge de webhook porte le tag d'une release, parfois
	// le titre d'une demande, et le jeton qui l'authentifie. En clair, tout cela
	// traverse le réseau lisible.
	AllowInsecure bool
	// Timeout borne l'appel entier. Un destinataire lent ne doit pas retenir le
	// moteur de plans : les gestes d'une étape partent ensemble, et un appel
	// suspendu bloquerait la conclusion de l'étape.
	Timeout time.Duration
	// MaxResponseBytes borne ce qu'on lit en retour.
	//
	// La réponse sert au diagnostic — un code, un extrait de corps — et non à
	// être exploitée. Sans borne, un destinataire hostile ferait grossir la
	// mémoire du worker par une réponse sans fin.
	MaxResponseBytes int64
}

// Valeurs par défaut, appliquées à ce qui n'est pas déclaré.
const (
	defaultTimeout          = 15 * time.Second
	defaultMaxResponseBytes = 8 << 10
)

// Client émet des appels sortants vers des cibles contrôlées.
type Client struct {
	http    *http.Client
	options Options
}

// New construit un client dont le transport refuse les cibles interdites.
func New(options Options) *Client {
	if options.Timeout <= 0 {
		options.Timeout = defaultTimeout
	}
	if options.MaxResponseBytes <= 0 {
		options.MaxResponseBytes = defaultMaxResponseBytes
	}

	// Le contrôle vit ici, dans le composeur, et non dans une vérification d'URL
	// faite en amont. La pile réseau appelle `Control` avec l'adresse
	// **résolue**, juste avant de connecter : il n'y a pas de seconde résolution
	// à tromper, et le réamorçage DNS n'a pas de prise.
	dialer := &net.Dialer{
		Timeout:   options.Timeout,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			return checkAddress(address, options.AllowPrivate)
		},
	}

	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   options.Timeout,
		ResponseHeaderTimeout: options.Timeout,
		// Les connexions ne sont pas réutilisées entre cibles : un pool partagé
		// ferait qu'une cible autorisée puis re-résolue vers une adresse
		// interdite emprunterait une connexion déjà ouverte, sans repasser par
		// le contrôle.
		DisableKeepAlives: true,
		Proxy:             nil,
	}

	return &Client{
		options: options,
		http: &http.Client{
			Transport: transport,
			Timeout:   options.Timeout,
			// Aucune redirection suivie. Une redirection est le contournement
			// évident d'un contrôle fait sur l'URL de départ : le destinataire
			// répond 302 vers `169.254.169.254`, et un client complaisant y va.
			//
			// Le composeur refuserait de connecter, mais échouer **ici** dit la
			// vraie cause plutôt qu'une adresse refusée que personne n'a saisie.
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				return fmt.Errorf("%w: redirection vers %s refusée",
					ErrBlockedTarget, safeURL(req.URL))
			},
		},
	}
}

// Response est ce qu'un appel a rapporté.
//
// Assez pour diagnostiquer, pas davantage : un code, et un extrait de corps.
// Les en-têtes **envoyés** n'y figurent jamais — ils portent l'authentification,
// et cette réponse finit dans une base que l'écran affiche.
type Response struct {
	Status int
	// Body est tronqué à [Options.MaxResponseBytes].
	Body string
}

// OK dit que le destinataire a accepté.
//
// Tout code 2xx, et rien d'autre. Un 3xx n'est pas un succès — la redirection
// n'ayant pas été suivie, personne n'a reçu la charge.
func (r Response) OK() bool { return r.Status >= 200 && r.Status < 300 }

// ErrInsecureTarget signale une cible en clair sans autorisation.
var ErrInsecureTarget = errors.New("cible en clair")

// Post émet une requête et rend ce qu'elle a rapporté.
//
// `headers` porte l'authentification. Ses valeurs ne sont **jamais** journalisées
// ni rendues : c'est la contrepartie de la règle qui veut qu'un champ sensible ne
// traverse pas un gabarit (doc 14 §14.7).
func (c *Client) Post(
	ctx context.Context, target string, headers map[string]string, body []byte,
) (Response, error) {
	parsed, err := c.checkURL(target)
	if err != nil {
		return Response{}, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(),
		bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("requête sortante: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}

	response, err := c.http.Do(request)
	if err != nil {
		// Le message d'erreur d'un client HTTP porte l'URL. Elle peut contenir un
		// jeton dans sa chaîne de requête — une pratique courante des webhooks de
		// messagerie —, d'où la forme expurgée.
		return Response{}, fmt.Errorf("appel de %s: %w", safeURL(parsed), unwrapURLError(err))
	}
	defer response.Body.Close()

	extract, err := io.ReadAll(io.LimitReader(response.Body, c.options.MaxResponseBytes))
	if err != nil {
		return Response{Status: response.StatusCode}, nil
	}
	return Response{Status: response.StatusCode, Body: strings.TrimSpace(string(extract))}, nil
}

// checkURL refuse ce qui ne doit même pas être composé.
//
// Le schéma se juge ici et non dans le composeur : celui-ci ne voit que des
// adresses, et ne saurait pas distinguer un appel en clair d'un appel chiffré.
func (c *Client) checkURL(target string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(target))
	if err != nil {
		return nil, fmt.Errorf("%w: URL illisible", ErrBlockedTarget)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("%w: URL sans hôte (%q)", ErrBlockedTarget, target)
	}

	switch parsed.Scheme {
	case "https":
	case "http":
		if !c.options.AllowInsecure {
			return nil, fmt.Errorf(
				"%w: %s — la charge et son jeton traverseraient le réseau en clair",
				ErrInsecureTarget, safeURL(parsed))
		}
	default:
		return nil, fmt.Errorf("%w: schéma %q (attendu https)", ErrBlockedTarget, parsed.Scheme)
	}

	return parsed, nil
}

// safeURL rend une URL nommable dans un message d'erreur.
//
// **`url.Redacted` ne suffit pas.** Elle masque le mot de passe de la partie
// utilisateur et laisse la chaîne de requête intacte — or c'est précisément là
// que les webhooks de messagerie portent leur jeton :
// `https://hooks.example.fr/services/T00/B00/xxxxxxxx` en chemin, ou
// `?token=…` en requête.
//
// Un message d'erreur finit dans un journal, et un journal se lit avec moins de
// précautions qu'une base. Ne restent donc que le schéma et l'hôte : de quoi
// dire quelle cible a échoué, sans emporter ce qui l'autorise.
func safeURL(target *url.URL) string {
	if target == nil {
		return ""
	}
	return target.Scheme + "://" + target.Host
}

// unwrapURLError dégage l'erreur utile d'un `*url.Error`.
//
// Sans cela, un refus de cible ressort enveloppé dans un message qui répète
// l'URL, et `errors.Is(err, ErrBlockedTarget)` cesse de fonctionner pour
// l'appelant — qui traiterait un refus comme une panne réseau.
func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}
	return err
}
