package http

import (
	"net/http"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/auth"
)

// NewAPIRouter monte les routes applicatives.
//
// L'autorisation est décidée ici, route par route, et jamais dans un handler :
// un contrôle dispersé finit toujours par manquer sur une route ajoutée plus
// tard. Le tableau ci-dessous est donc la référence de ce qui est protégé.
func NewAPIRouter(
	handler *AuthHandler, runs *RunsHandler, logs *LogsHandler, queue *QueueHandler,
	releases *ReleasesHandler, middleware *Middleware,
) http.Handler {
	mux := http.NewServeMux()

	// Routes publiques : elles servent précisément à obtenir une session.
	mux.HandleFunc("GET /auth/login", handler.Login)
	mux.HandleFunc("GET /auth/callback", handler.Callback)

	// La déconnexion modifie l'état, donc passe par le contrôle CSRF, mais
	// n'exige pas de rôle : un utilisateur sans rôle doit pouvoir se déconnecter.
	mux.Handle("POST /auth/logout", Chain(http.HandlerFunc(handler.Logout), middleware.CSRF))

	mux.Handle("GET /me", Chain(http.HandlerFunc(handler.Me), middleware.Authenticate))

	// Les exécutions sont en lecture pour tous les rôles : constater ce que les
	// pipelines ont fait ne demande aucun privilège, et c'est le premier usage
	// réellement utile du dashboard.
	mux.Handle("GET /runs", Chain(http.HandlerFunc(runs.List),
		middleware.Authenticate, middleware.Require(auth.PermRead)))
	mux.Handle("GET /runs/events", Chain(http.HandlerFunc(runs.Stream),
		middleware.Authenticate, middleware.Require(auth.PermRead)))
	// Déclarée après /runs/events : le ServeMux choisit le motif le plus
	// spécifique, mais l'ordre de lecture reste plus clair ainsi.
	mux.Handle("GET /runs/{run}", Chain(http.HandlerFunc(runs.Get),
		middleware.Authenticate, middleware.Require(auth.PermRead)))

	// Les journaux se lisent par plage, et se suivent par flux. Les deux passent
	// par le même lecteur : un client peut suivre un run puis remonter dans son
	// historique sans changer de mécanisme.
	mux.Handle("GET /runs/{run}/tasks/{task}/log", Chain(http.HandlerFunc(logs.Read),
		middleware.Authenticate, middleware.Require(auth.PermRead)))
	mux.Handle("GET /runs/{run}/tasks/{task}/log/stream", Chain(http.HandlerFunc(logs.Stream),
		middleware.Authenticate, middleware.Require(auth.PermRead)))

	// La file est en lecture pour tous : comprendre pourquoi une exécution
	// attend ne demande aucun privilège, et c'est justement ce qui évite les
	// relances inutiles.
	mux.Handle("GET /queue", Chain(http.HandlerFunc(queue.List),
		middleware.Authenticate, middleware.Require(auth.PermRead)))

	// Lancer et annuler modifient l'état : contrôle CSRF en plus de la
	// permission, et `pipeline:run` plutôt que `read`.
	mux.Handle("POST /queue", Chain(http.HandlerFunc(queue.Launch),
		middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermPipelineRun)))
	mux.Handle("POST /queue/{id}/cancel", Chain(http.HandlerFunc(queue.Cancel),
		middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermPipelineCancel)))

	// Les releases se lisent comme les runs. Le détail expose aussi, pour chaque
	// environnement, si la release peut y aller — ce qui dépend de l'identité qui
	// demande, et se calcule donc ici plutôt que dans la page.
	mux.Handle("GET /releases", Chain(http.HandlerFunc(releases.List),
		middleware.Authenticate, middleware.Require(auth.PermRead)))
	mux.Handle("GET /releases/{tag}", Chain(http.HandlerFunc(releases.Get),
		middleware.Authenticate, middleware.Require(auth.PermRead)))

	// La route exige `release:create` ; la création d'une release **finale** exige
	// davantage, et le handler le vérifie — la nature est dans le corps, pas dans
	// le chemin, et un middleware ne peut pas la lire sans consommer la requête.
	mux.Handle("POST /releases", Chain(http.HandlerFunc(releases.Create),
		middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermReleaseCreate)))

	// L'environnement est dans le chemin et non dans le corps, précisément pour
	// que l'autorisation — qui est par environnement — reste décidée ici.
	mux.Handle("POST /releases/{tag}/promote/{environment}", Chain(http.HandlerFunc(releases.Promote),
		middleware.Authenticate, middleware.CSRF, middleware.RequireDeploy("environment")))

	// La vue d'ensemble est en lecture pour tous : savoir ce qui tourne où ne
	// demande aucun privilège.
	mux.Handle("GET /environments", Chain(http.HandlerFunc(releases.Environments),
		middleware.Authenticate, middleware.Require(auth.PermRead)))

	return mux
}
