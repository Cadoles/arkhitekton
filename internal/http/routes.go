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
	releases *ReleasesHandler, benches *BenchHandler, tests *TestsHandler,
	signoffs *SignoffsHandler, versions *VersionsHandler, syncAdmin *SyncAdminHandler,
	forge *ForgeHandler, admin *AdminHandler, projects *ProjectsHandler,
	middleware *Middleware,
) http.Handler {
	mux := http.NewServeMux()

	// Les routes qui portent des objets **rattachés à un projet** — releases et
	// versions — le nomment dans leur chemin. C'est ce que la migration 0025
	// impose : leur identité n'existe que dans un projet, et une URL qui ne le dit
	// pas désignerait deux choses le jour où un second existera.
	//
	// La file, les exécutions et les environnements gardent leur chemin. Ce n'est
	// pas un oubli : rien en base ne les rattache à un projet, et les préfixer
	// trancherait par la bande des questions qui ne le sont pas — une file
	// est-elle propre à un projet quand ses plafonds sont ceux du cluster ? un
	// environnement de qualification est-il partagé ? (doc 12 §12.4).

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
	// Le journal entier, en texte : consultable dans un onglet, enregistrable en
	// fichier. Même permission que la lecture par plage — c'est la même donnée,
	// servie autrement.
	mux.Handle("GET /runs/{run}/tasks/{task}/log.txt", Chain(http.HandlerFunc(logs.Raw),
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
	// Les plafonds de file sont un réglage d'exploitation : relever engage le
	// cluster, baisser rallonge toute vérification. Réservés à l'administration,
	// au même titre que les rôles.
	mux.Handle("PUT /queue/limits/{kind}", Chain(http.HandlerFunc(queue.SetLimit),
		middleware.Authenticate, middleware.Require(auth.PermAdmin), middleware.CSRF))
	mux.Handle("DELETE /queue/limits/{kind}", Chain(http.HandlerFunc(queue.ClearLimit),
		middleware.Authenticate, middleware.Require(auth.PermAdmin), middleware.CSRF))

	mux.Handle("POST /queue/{id}/cancel", Chain(http.HandlerFunc(queue.Cancel),
		middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermPipelineCancel)))

	// Les releases se lisent comme les runs. Le détail expose aussi, pour chaque
	// environnement, si la release peut y aller — ce qui dépend de l'identité qui
	// demande, et se calcule donc ici plutôt que dans la page.
	mux.Handle("GET /projects/{project}/releases", Chain(http.HandlerFunc(releases.List),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))
	mux.Handle("GET /projects/{project}/releases/{tag}", Chain(http.HandlerFunc(releases.Get),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))

	// La route exige `release:create` ; la création d'une release **finale** exige
	// davantage, et le handler le vérifie — la nature est dans le corps, pas dans
	// le chemin, et un middleware ne peut pas la lire sans consommer la requête.
	mux.Handle("POST /projects/{project}/releases", Chain(http.HandlerFunc(releases.Create),
		middleware.Authenticate, middleware.CSRF, middleware.RequireIn(auth.PermReleaseCreate)))

	// L'environnement est dans le chemin et non dans le corps, précisément pour
	// que l'autorisation — qui est par environnement — reste décidée ici.
	mux.Handle("POST /projects/{project}/releases/{tag}/promote/{environment}", Chain(http.HandlerFunc(releases.Promote),
		middleware.Authenticate, middleware.CSRF, middleware.RequireDeploy("environment")))

	// Valider une release engage ce qui partira chez l'utilisateur : même
	// permission que produire une release finale, et non `release:edit`. C'est
	// aussi la seule transition que rien n'automatise — un pipeline vert dit que
	// le code fonctionne, pas qu'on veut le livrer.
	mux.Handle("POST /projects/{project}/releases/{tag}/release", Chain(http.HandlerFunc(releases.Release),
		middleware.Authenticate, middleware.CSRF, middleware.RequireIn(auth.PermReleaseFinal)))

	// Relancer une construction ne crée pas de version : même permission que la
	// création, et non celle de livrer. C'est le geste qui répare un échec
	// d'infrastructure sans faire porter au numéro de version l'historique des
	// pannes du cluster.
	mux.Handle("POST /projects/{project}/releases/{tag}/build", Chain(http.HandlerFunc(releases.Rebuild),
		middleware.Authenticate, middleware.CSRF, middleware.RequireIn(auth.PermReleaseCreate)))

	// Relancer une seule vérification. Même permission que la reconstruction :
	// c'est du calcul dans le cluster, pas une livraison. Moins engageant, même :
	// rien n'est republié, l'image ne bouge pas.
	mux.Handle("POST /projects/{project}/releases/{tag}/verify/{kind}", Chain(http.HandlerFunc(releases.Verify),
		middleware.Authenticate, middleware.CSRF, middleware.RequireIn(auth.PermReleaseCreate)))

	// La note de release se lit avec la release et s'édite comme les actions
	// manuelles : `release:edit` et non `release:create:final`. Composer la note
	// est le travail de qui produit le livrable, pas de qui décide de le livrer.
	mux.Handle("GET /projects/{project}/releases/{tag}/notes", Chain(http.HandlerFunc(releases.Notes),
		middleware.Authenticate))
	mux.Handle("POST /projects/{project}/releases/{tag}/notes/{issue}", Chain(http.HandlerFunc(releases.AddNote),
		middleware.Authenticate, middleware.CSRF, middleware.RequireIn(auth.PermReleaseEdit)))
	mux.Handle("DELETE /projects/{project}/releases/{tag}/notes/{issue}", Chain(http.HandlerFunc(releases.RemoveNote),
		middleware.Authenticate, middleware.CSRF, middleware.RequireIn(auth.PermReleaseEdit)))

	// Les actions manuelles sont éditables par ceux qui produisent le livrable,
	// pas seulement par ceux qui peuvent livrer : `release:edit` et non
	// `release:create:final`.
	mux.Handle("PUT /projects/{project}/releases/{tag}/actions", Chain(http.HandlerFunc(releases.UpdateActions),
		middleware.Authenticate, middleware.CSRF, middleware.RequireIn(auth.PermReleaseEdit)))

	// Les livrables se listent et se téléchargent en lecture ; les produire est un
	// geste de livraison, donc `release:export`.
	mux.Handle("GET /projects/{project}/releases/{tag}/exports", Chain(http.HandlerFunc(releases.Exports),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))
	mux.Handle("GET /projects/{project}/releases/{tag}/exports/{artifact}/download", Chain(http.HandlerFunc(releases.Download),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))
	mux.Handle("POST /projects/{project}/releases/{tag}/exports", Chain(http.HandlerFunc(releases.Export),
		middleware.Authenticate, middleware.CSRF, middleware.RequireIn(auth.PermReleaseExport)))

	// Les résultats de tests et le score sont en lecture pour tous : c'est la
	// première chose qu'on vient regarder avant de décider quoi que ce soit.
	mux.Handle("GET /projects/{project}/releases/{tag}/tests", Chain(http.HandlerFunc(tests.Tests),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))
	mux.Handle("GET /projects/{project}/releases/{tag}/tests/{family}", Chain(http.HandlerFunc(tests.Family),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))
	mux.Handle("GET /projects/{project}/releases/{tag}/security", Chain(http.HandlerFunc(tests.Security),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))

	// La validation de recette se lit sans privilège — savoir si le client a
	// validé intéresse tout le monde. Se prononcer est en revanche une décision de
	// livraison : `release:edit`, la même permission que les actions manuelles,
	// parce que ce sont les mêmes personnes qui conduisent une recette. Exiger
	// `release:create:final` reviendrait à réserver la validation à ceux qui
	// livrent, alors que valider et livrer sont justement les deux gestes qu'on
	// souhaite voir accomplis par des personnes distinctes.
	mux.Handle("GET /projects/{project}/releases/{tag}/signoffs", Chain(http.HandlerFunc(signoffs.List),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))
	mux.Handle("PUT /projects/{project}/releases/{tag}/signoffs/{env}", Chain(http.HandlerFunc(signoffs.Set),
		middleware.Authenticate, middleware.CSRF, middleware.RequireIn(auth.PermReleaseEdit)))
	mux.Handle("DELETE /projects/{project}/releases/{tag}/signoffs/{env}", Chain(http.HandlerFunc(signoffs.Clear),
		middleware.Authenticate, middleware.CSRF, middleware.RequireIn(auth.PermReleaseEdit)))

	// Les campagnes de charge sont en lecture pour tous. Épingler une baseline
	// déplace en revanche la référence de tous les verdicts suivants : c'est une
	// décision de livraison, pas une préférence d'affichage.
	mux.Handle("GET /projects/{project}/releases/{tag}/bench", Chain(http.HandlerFunc(benches.Bench),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))
	mux.Handle("POST /projects/{project}/releases/{tag}/bench/{measure}/baseline", Chain(http.HandlerFunc(benches.PinBaseline),
		middleware.Authenticate, middleware.CSRF, middleware.RequireIn(auth.PermReleaseFinal)))

	// La vue d'ensemble est en lecture pour tous : savoir ce qui tourne où ne
	// demande aucun privilège.
	mux.Handle("GET /environments", Chain(http.HandlerFunc(releases.Environments),
		middleware.Authenticate, middleware.Require(auth.PermRead)))
	mux.Handle("GET /environments/{environment}", Chain(http.HandlerFunc(releases.Environment),
		middleware.Authenticate, middleware.Require(auth.PermRead)))
	mux.Handle("GET /environments/{environment}/history", Chain(http.HandlerFunc(releases.History),
		middleware.Authenticate, middleware.Require(auth.PermRead)))

	// L'administration exige `admin`, et rien de moins : attribuer un rôle est ce
	// qui permet d'en attribuer d'autres, donc la seule permission qui se
	// propage. Le journal d'audit y est rattaché parce qu'il sert d'abord à
	// contrôler ces attributions.
	mux.Handle("GET /admin/roles", Chain(http.HandlerFunc(admin.Roles),
		middleware.Authenticate, middleware.Require(auth.PermAdmin)))
	mux.Handle("POST /admin/roles", Chain(http.HandlerFunc(admin.Grant),
		middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermAdmin)))
	mux.Handle("DELETE /admin/roles/{subject}", Chain(http.HandlerFunc(admin.Revoke),
		middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermAdmin)))
	mux.Handle("GET /admin/audit", Chain(http.HandlerFunc(admin.Audit),
		middleware.Authenticate, middleware.Require(auth.PermAdmin)))

	// Les versions se lisent sans privilège : le périmètre d'une version est
	// l'information la plus consultée, et la première qu'on vient chercher avant de
	// décider quoi que ce soit.
	mux.Handle("GET /projects/{project}/versions", Chain(http.HandlerFunc(versions.List),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))
	mux.Handle("GET /projects/{project}/versions/{id}", Chain(http.HandlerFunc(versions.Get),
		middleware.Authenticate, middleware.RequireIn(auth.PermRead)))

	// Les références du dépôt servent le formulaire de création de release. En
	// lecture pour tous : ce sont des noms de branches, pas des secrets, et le
	// jeton qui les lit ne quitte jamais le serveur.
	// Les dépôts pilotés se lisent avec les autres réglages, et s'écrivent comme
	// eux : déclarer un dépôt dit ce que le dashboard ira cloner, ce qui n'est
	// pas une décision d'exploitation courante.
	mux.Handle("GET /projects", Chain(http.HandlerFunc(projects.List),
		middleware.Authenticate, middleware.Require(auth.PermAdmin)))
	mux.Handle("POST /projects", Chain(http.HandlerFunc(projects.Create),
		middleware.Authenticate, middleware.Require(auth.PermAdmin), middleware.CSRF))
	mux.Handle("PUT /projects/{id}", Chain(http.HandlerFunc(projects.Update),
		middleware.Authenticate, middleware.Require(auth.PermAdmin), middleware.CSRF))
	mux.Handle("DELETE /projects/{id}", Chain(http.HandlerFunc(projects.Delete),
		middleware.Authenticate, middleware.Require(auth.PermAdmin), middleware.CSRF))
	// Le test sort du dashboard : il appelle la forge. `POST` et contrôle CSRF,
	// bien qu'il n'écrive que son propre constat.
	mux.Handle("POST /projects/{id}/probe", Chain(http.HandlerFunc(projects.Probe),
		middleware.Authenticate, middleware.Require(auth.PermAdmin), middleware.CSRF))

	// La rotation d'une clé de déploiement est une action sortante : l'ancienne
	// devient caduque à l'instant même, et le dépôt cesse d'être clonable tant
	// que la nouvelle n'y est pas collée. `POST` et contrôle CSRF.
	mux.Handle("POST /projects/{id}/deploy-key", Chain(http.HandlerFunc(projects.RotateDeployKey),
		middleware.Authenticate, middleware.Require(auth.PermAdmin), middleware.CSRF))

	// L'activation dit que la clé a été collée dans la forge. Elle change ce que
	// les pipelines montent : `POST` et contrôle CSRF.
	mux.Handle("POST /projects/{id}/deploy-key/active", Chain(http.HandlerFunc(projects.ActivateDeployKey),
		middleware.Authenticate, middleware.Require(auth.PermAdmin), middleware.CSRF))

	mux.Handle("GET /forge/refs", Chain(http.HandlerFunc(forge.Refs),
		middleware.Authenticate, middleware.Require(auth.PermRead)))

	// L'état des synchronisations exige `admin` : il porte les messages d'erreur de
	// l'instance Redmine du client, et ceux-ci renseignent sur son infrastructure.
	//
	// Changer la projection d'un statut déplace un taux de complétion affiché au
	// client, et relancer une synchronisation complète sollicite un service tiers :
	// deux gestes qui relèvent de l'administration, pas de la consultation.
	mux.Handle("GET /admin/sync", Chain(http.HandlerFunc(syncAdmin.State),
		middleware.Authenticate, middleware.Require(auth.PermAdmin)))
	mux.Handle("POST /admin/sync/redmine/resync", Chain(http.HandlerFunc(syncAdmin.Resync),
		middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermAdmin)))
	mux.Handle("PUT /admin/sync/redmine/mapping/{status}", Chain(http.HandlerFunc(syncAdmin.SetMapping),
		middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermAdmin)))

	return mux
}
