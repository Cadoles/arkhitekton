package http

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/auth"
)

const (
	sessionCookieName = "mse_session"
	csrfCookieName    = "mse_csrf"
	csrfHeaderName    = "X-CSRF-Token"
)

// Sessions décrit ce que les handlers attendent du magasin de sessions.
type Sessions interface {
	Create(ctx context.Context, subject, username, email string) (auth.Session, error)
	// ApplyBootstrap promeut administrateur un compte déclaré en configuration.
	// Appelée à la connexion : le sujet du fournisseur d'identité n'est pas
	// connaissable avant.
	ApplyBootstrap(ctx context.Context, subject, username string) (string, error)
	Lookup(ctx context.Context, id string) (auth.Session, auth.Role, error)
	Revoke(ctx context.Context, id string) error
}

// AuthHandler porte le flux d'authentification.
type AuthHandler struct {
	exchanger auth.Exchanger
	flows     Flows
	sessions  Sessions
	logger    *slog.Logger
	// secureCookies doit valoir true partout sauf en développement sur http.
	secureCookies bool
	sessionTTL    time.Duration
	// projects énumère les projets déclarés, rendus avec l'identité. Nil quand il
	// n'est pas branché : `/me` répond alors sans liste, et l'interface le dit
	// plutôt que de composer des URL creuses.
	projects projectLister
}

// WithProjects branche la résolution du projet courant sur `/me`.
func (h *AuthHandler) WithProjects(projects projectLister) *AuthHandler {
	h.projects = projects
	return h
}

func NewAuthHandler(
	exchanger auth.Exchanger,
	flows Flows,
	sessions Sessions,
	logger *slog.Logger,
	secureCookies bool,
	sessionTTL time.Duration,
) *AuthHandler {
	return &AuthHandler{
		exchanger:     exchanger,
		flows:         flows,
		sessions:      sessions,
		logger:        logger,
		secureCookies: secureCookies,
		sessionTTL:    sessionTTL,
	}
}

// Flows est ce que le handler attend du magasin de transactions.
//
// Une interface, et non le type concret : le magasin en mémoire suffit aux tests,
// tandis que la production exige un stockage partagé — deux répliques d'api sans
// affinité de session servent `/auth/login` et `/auth/callback` par des instances
// différentes, et l'état serait introuvable une fois sur deux.
type Flows interface {
	Begin(ctx context.Context, redirectAfter string) (auth.FlowState, error)
	Consume(ctx context.Context, state string) (auth.FlowState, error)
}

// Login ouvre une transaction et redirige vers le fournisseur.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	flow, err := h.flows.Begin(r.Context(), safeRedirect(r.URL.Query().Get("redirect")))
	if err != nil {
		h.logger.Error("ouverture de transaction d'authentification", "error", err)
		writeProblem(w, http.StatusInternalServerError, "Authentification indisponible", "")
		return
	}

	http.Redirect(w, r,
		h.exchanger.AuthCodeURL(flow.State, flow.Nonce, auth.CodeChallenge(flow.CodeVerifier)),
		http.StatusFound)
}

// Callback traite le retour du fournisseur.
func (h *AuthHandler) Callback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	// Le fournisseur signale un refus par `error` : le distinguer d'un échec
	// technique évite d'afficher « erreur interne » à qui a simplement cliqué
	// sur « Annuler ».
	if providerError := query.Get("error"); providerError != "" {
		h.logger.Info("authentification refusée par le fournisseur", "error", providerError)
		writeProblem(w, http.StatusUnauthorized, "Authentification refusée",
			"Le fournisseur d'identité a refusé la demande.")
		return
	}

	flow, err := h.flows.Consume(r.Context(), query.Get("state"))
	if err != nil {
		// Rejeu, expiration ou state forgé : tous se traitent pareil, et sans
		// détail — distinguer les cas donnerait un oracle.
		h.logger.Warn("state d'authentification rejeté")
		writeProblem(w, http.StatusBadRequest, "Session d'authentification expirée",
			"Relancez la connexion.")
		return
	}

	code := query.Get("code")
	if code == "" {
		writeProblem(w, http.StatusBadRequest, "Code d'autorisation absent", "")
		return
	}

	claims, err := h.exchanger.Exchange(r.Context(), code, flow.CodeVerifier, flow.Nonce)
	if err != nil {
		h.logger.Error("échange du code d'autorisation", "error", err)
		writeProblem(w, http.StatusUnauthorized, "Authentification refusée", "")
		return
	}

	// Avant la création de session : c'est ici que le sujet du fournisseur est
	// enfin connu, et donc le seul moment où un rôle déclaré à l'avance peut lui
	// être rattaché.
	if role, err := h.sessions.ApplyBootstrap(
		r.Context(), claims.Subject, claims.Username); err != nil {
		// L'amorçage n'est pas bloquant : échouer ici priverait de connexion un
		// utilisateur qui a peut-être déjà un rôle.
		h.logger.Error("amorçage d'un rôle", "subject", claims.Subject, "error", err)
	} else if role != "" {
		h.logger.Info("rôle d'amorçage attribué",
			"subject", claims.Subject, "username", claims.Username, "role", role)
	}

	session, err := h.sessions.Create(r.Context(), claims.Subject, claims.Username, claims.Email)
	if err != nil {
		h.logger.Error("création de session", "error", err)
		writeProblem(w, http.StatusInternalServerError, "Session indisponible", "")
		return
	}

	h.setSessionCookie(w, session.ID)
	if err := h.setCSRFCookie(w); err != nil {
		h.logger.Error("génération du jeton CSRF", "error", err)
		writeProblem(w, http.StatusInternalServerError, "Session indisponible", "")
		return
	}

	h.logger.Info("session ouverte", "subject", claims.Subject, "username", claims.Username)
	http.Redirect(w, r, flow.RedirectAfter, http.StatusFound)
}

// Logout révoque la session et efface les cookies.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if err := h.sessions.Revoke(r.Context(), cookie.Value); err != nil {
			// La révocation en base a échoué : effacer quand même le cookie
			// serait trompeur, l'utilisateur se croirait déconnecté alors que sa
			// session reste utilisable par qui la détiendrait.
			h.logger.Error("révocation de session", "error", err)
			writeProblem(w, http.StatusInternalServerError, "Déconnexion incomplète",
				"La session n'a pas pu être révoquée, réessayez.")
			return
		}
	}

	h.clearCookie(w, sessionCookieName)
	h.clearCookie(w, csrfCookieName)
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged-out"})
}

// Me rend l'identité et les permissions effectives.
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
		return
	}
	// Le projet courant accompagne l'identité, et c'est le seul endroit où toute
	// personne connectée peut le lire : la liste des projets exige `admin`, alors
	// que l'interface a besoin de composer ses URL dès le premier écran.
	//
	// Un seul projet aujourd'hui. Le jour où il y en aura plusieurs, c'est ici que
	// le choix courant sera rendu — pas dans une seconde route à consulter.
	payload := map[string]any{
		"subject":  identity.Subject,
		"username": identity.Username,
		"email":    identity.Email,
		"role":     identity.Role,
	}
	if h.projects != nil {
		// La **liste**, et non un projet unique. `/me` rendait « le projet
		// constructible », ce qui supposait qu'il n'y en ait qu'un : déclarer un
		// second faisait échouer la résolution, et l'interface se retrouvait sans
		// aucune URL composable — tout l'écran des releases avec.
		//
		// C'est aussi ce que le multi-projet veut dire côté interface : le choix
		// appartient à qui regarde, pas au dashboard.
		projects, err := h.projects.ListProjects(r.Context())
		if err != nil && h.logger != nil {
			h.logger.Warn("liste des projets indisponible", "error", err)
		}
		visible := make([]map[string]string, 0, len(projects))
		for _, project := range projects {
			visible = append(visible, map[string]string{
				"slug": project.Slug, "label": project.Label,
			})
		}
		payload["projects"] = visible
	}
	writeJSON(w, http.StatusOK, payload)
}

func (h *AuthHandler) setSessionCookie(w http.ResponseWriter, id string) {
	http.SetCookie(w, &http.Cookie{
		Name:  sessionCookieName,
		Value: id,
		Path:  "/",
		// HttpOnly : un jeton lisible par un script de la page est un jeton
		// volable par n'importe quelle injection.
		HttpOnly: true,
		Secure:   h.secureCookies,
		// Lax et non Strict : Strict empêcherait le cookie d'accompagner la
		// redirection de retour du fournisseur, et la session paraîtrait perdue
		// juste après une connexion réussie.
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(h.sessionTTL.Seconds()),
	})
}

// setCSRFCookie pose un jeton lisible par le frontend, à renvoyer en en-tête sur
// les méthodes non sûres. SameSite=Lax couvre la navigation intersite, pas les
// requêtes émises par une page tierce déjà chargée.
func (h *AuthHandler) setCSRFCookie(w http.ResponseWriter) error {
	token, err := auth.NewCSRFToken()
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:  csrfCookieName,
		Value: token,
		Path:  "/",
		// Volontairement lisible par le script : c'est le principe de la double
		// soumission. Sa valeur ne donne rien sans le cookie de session.
		HttpOnly: false,
		Secure:   h.secureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(h.sessionTTL.Seconds()),
	})
	return nil
}

func (h *AuthHandler) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: name == sessionCookieName,
		Secure:   h.secureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// safeRedirect n'accepte qu'un chemin interne.
//
// Sans ce filtrage, `?redirect=https://exemple.invalide` transformerait la page
// de connexion en redirecteur ouvert : un lien vers le dashboard, hébergé sur le
// bon domaine, renverrait l'utilisateur authentifié vers un site tiers. Les
// chemins protocol-relative (`//hôte`) comptent comme externes.
func safeRedirect(target string) string {
	const fallback = "/"

	if target == "" || !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") {
		return fallback
	}
	// `/\hôte` est interprété comme une URL absolue par certains navigateurs.
	if strings.HasPrefix(target, "/\\") {
		return fallback
	}
	return target
}
