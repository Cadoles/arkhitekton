package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/deploykeys"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * La boucle qui pourvoit les projets en clés de déploiement.
 *
 * Elle vit dans le worker et non dans l'API, et ce n'est pas un détail de
 * répartition. L'API tourne avec `automountServiceAccountToken: false` — elle
 * n'a aucun jeton de cluster, délibérément, parce que c'est la surface exposée.
 * Lui en accorder un pour la commodité d'une pose synchrone à la déclaration
 * serait un recul de sécurité déguisé en fonctionnalité.
 *
 * L'API **demande** donc, en base ; le worker réalise, sous le bail de leader.
 * C'est aussi ce qui donne au lot G sa boucle de réconciliation : un projet sans
 * clé en reçoit une, une rotation demandée s'exécute, et un redémarrage rattrape
 * ce qu'une panne a interrompu (doc 12 §12.7).
 */

// keyInterval espace deux passages de la boucle.
//
// Long, et c'est voulu : la pose est déclenchée par une action humaine rare —
// déclarer un projet, demander une rotation — et non par un flux. Sonder plus
// souvent multiplierait les requêtes pour rattraper au mieux quelques secondes
// sur un geste qui en prend déjà plusieurs.
const keyInterval = time.Minute

// KeyStore est ce que la boucle attend de la base.
type KeyStore interface {
	ProjectsAwaitingKey(ctx context.Context) ([]store.PendingKey, error)
	SetDeployKey(ctx context.Context, projectID int64, public string) error
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// KeyIssuer pose la clé d'un projet dans le cluster.
type KeyIssuer interface {
	Issue(ctx context.Context, slug string, known bool) (string, error)
}

// KeyReconciler pourvoit les projets qui attendent une clé.
type KeyReconciler struct {
	store  KeyStore
	keys   KeyIssuer
	logger *slog.Logger
}

func NewKeyReconciler(projects KeyStore, keys KeyIssuer, logger *slog.Logger) *KeyReconciler {
	return &KeyReconciler{store: projects, keys: keys, logger: logger}
}

// Run fait tourner la boucle jusqu'à annulation.
//
// Un premier passage immédiat : un projet déclaré pendant que le worker était
// arrêté ne doit pas attendre le prochain tick pour recevoir sa clé.
func (r *KeyReconciler) Run(ctx context.Context) error {
	ticker := time.NewTicker(keyInterval)
	defer ticker.Stop()

	for {
		r.once(ctx)

		select {
		case <-ctx.Done():
			r.logger.Info("arrêt de la pose des clés de déploiement")
			return nil
		case <-ticker.C:
		}
	}
}

func (r *KeyReconciler) once(ctx context.Context) {
	pending, err := r.store.ProjectsAwaitingKey(ctx)
	if err != nil {
		r.logger.Error("projets en attente de clé", "error", err)
		return
	}

	for _, project := range pending {
		r.issue(ctx, project)
	}
}

// issue pose la clé d'un projet, et n'interrompt pas les autres si elle échoue.
//
// Un projet dont le Secret porte un nom déjà pris est un cas qu'un humain doit
// trancher : le signaler et passer au suivant vaut mieux que de bloquer la
// boucle sur lui, ce qui priverait de clé tous ceux qui viennent après.
func (r *KeyReconciler) issue(ctx context.Context, project store.PendingKey) {
	public, err := r.keys.Issue(ctx, project.Slug, project.Known)
	switch {
	case errors.Is(err, deploykeys.ErrNameTaken):
		r.logger.Error("clé de déploiement non posée",
			"projet", project.Slug,
			"raison", "un secret porte déjà ce nom et n'a pas été créé par le dashboard",
			"consequence", "le projet emploie la clé partagée du déploiement")
		return
	case err != nil:
		r.logger.Error("pose de la clé de déploiement",
			"projet", project.Slug, "error", err)
		return
	}

	// La base **après** le cluster. L'inverse ferait croire à une clé posée si le
	// dépôt échouait, et une rotation ultérieure remplacerait alors un Secret qui
	// n'existe pas — ou celui d'un tiers, puisque la colonne aurait affirmé une
	// propriété que le dashboard n'a pas.
	if err := r.store.SetDeployKey(ctx, project.ID, public); err != nil {
		r.logger.Error("enregistrement de la clé de déploiement",
			"projet", project.Slug, "error", err)
		return
	}

	// Journalisée comme une action sortante : « qui a changé la clé, et quand »
	// relève de l'audit au même titre qu'une promotion. L'acteur est le dashboard
	// lui-même — la demande, elle, porte le nom de qui l'a faite.
	_ = r.store.Audit(ctx, "dashboard", store.AuditProjectSet, project.Slug,
		map[string]any{"deploy_key": "posée", "rotation": project.Known})

	r.logger.Info("clé de déploiement posée",
		"projet", project.Slug, "rotation", project.Known)
}
