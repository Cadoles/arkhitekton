package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

// LeaseName est le nom du Lease qui arbitre entre workers.
const LeaseName = "mse-dashboard-worker"

// LeaderConfig décrit l'élection.
type LeaderConfig struct {
	// Namespace du Lease : celui du dashboard, jamais celui des pipelines. La
	// Role accordée ne couvre que le premier (doc 07 §7.3).
	Namespace string
	// Identity doit être unique par processus. Le nom du pod le garantit ; en
	// développement, le nom d'hôte suffit.
	Identity string
}

// LeaderIdentity dérive une identité du contexte d'exécution.
//
// POD_NAME est injecté par le Deployment. Sans lui, le journal dirait qu'un
// leader existe sans dire lequel — précisément l'information utile quand deux
// processus se disputent la place.
func LeaderIdentity() string {
	if pod := os.Getenv("POD_NAME"); pod != "" {
		return pod
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		return host + "-local"
	}
	return "inconnu"
}

// RunAsLeader acquiert le Lease puis exécute `work`. Le contexte passé à `work`
// est annulé dès la perte du bail.
//
// Les durées suivent les valeurs recommandées par client-go, avec un rapport
// volontairement large entre le bail et la période de renouvellement : un worker
// occupé — la collecte de logs sature ses goroutines — ne doit pas perdre le bail
// pour un renouvellement en retard de deux secondes. Perdre le bail à tort
// signifie une bascule, donc une interruption de la collecte, donc des lignes
// perdues.
func RunAsLeader(
	ctx context.Context,
	kube kubernetes.Interface,
	cfg LeaderConfig,
	logger *slog.Logger,
	work func(context.Context) error,
) error {
	lock := &resourcelock.LeaseLock{
		LeaseMeta:  metav1.ObjectMeta{Name: LeaseName, Namespace: cfg.Namespace},
		Client:     kube.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{Identity: cfg.Identity},
	}

	// workErr est renseignée par la goroutine de travail et lue après la fin de
	// l'élection : `leaderelection.Run` ne rend pas d'erreur.
	var workErr error

	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock:            lock,
		LeaseDuration:   30 * time.Second,
		RenewDeadline:   20 * time.Second,
		RetryPeriod:     5 * time.Second,
		ReleaseOnCancel: true,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(leadingCtx context.Context) {
				logger.Info("bail acquis", "identity", cfg.Identity, "lease", LeaseName)
				if err := work(leadingCtx); err != nil {
					workErr = err
					logger.Error("boucles de fond interrompues", "error", err)
				}
			},
			OnStoppedLeading: func() {
				// Ni panique ni redémarrage : le processus reste candidat. Un
				// bail perdu pendant une bascule réseau est un incident normal,
				// et sortir en erreur transformerait une seconde d'indisponibilité
				// en boucle de crash.
				logger.Warn("bail perdu", "identity", cfg.Identity)
			},
			OnNewLeader: func(identity string) {
				if identity == cfg.Identity {
					return
				}
				logger.Info("un autre worker mène", "leader", identity)
			},
		},
	})
	if err != nil {
		return fmt.Errorf("élection de leader: %w", err)
	}

	elector.Run(ctx)
	return workErr
}
