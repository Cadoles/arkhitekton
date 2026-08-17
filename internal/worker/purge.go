package worker

import (
	"context"
	"log/slog"
	"time"
)

// purgeInterval borne la fréquence de la purge.
//
// Un quart d'heure : ces tables ne grandissent qu'au rythme des connexions
// humaines, et purger plus souvent coûterait des requêtes pour rien. Plus
// rarement laisserait s'accumuler les transactions abandonnées, dont chacune
// naît d'une connexion commencée puis interrompue.
const purgeInterval = 15 * time.Minute

// Expirable est ce que la purge sait nettoyer.
//
// Les deux ensemble, parce que les deux ont la même cause d'accumulation : un
// utilisateur qui ferme son onglet. Les séparer donnerait deux boucles au même
// rythme, sur la même base.
type Expirable interface {
	// PurgeExpired supprime les sessions échues.
	PurgeExpired(ctx context.Context) (int64, error)
}

// Flows nettoie les transactions d'authentification abandonnées.
type Flows interface {
	PurgeExpiredFlows(ctx context.Context) (int64, error)
}

// Purger supprime périodiquement ce qui a expiré.
//
// La boucle manquait. `PurgeExpired` existait, était documentée comme « appelée
// périodiquement par le worker », et personne ne l'appelait : les sessions échues
// restaient en base indéfiniment. Une documentation qui décrit un comportement
// absent est pire qu'une absence de documentation — elle dispense d'aller voir.
type Purger struct {
	sessions Expirable
	flows    Flows
	logger   *slog.Logger
}

func NewPurger(sessions Expirable, flows Flows, logger *slog.Logger) *Purger {
	return &Purger{sessions: sessions, flows: flows, logger: logger}
}

// Run purge jusqu'à annulation.
//
// Une purge en échec ne fait pas tomber la boucle : elle est un entretien, pas
// une fonction. Perdre le worker parce qu'une suppression a échoué arrêterait
// l'ordonnancement et l'observation, qui n'ont rien à voir.
func (p *Purger) Run(ctx context.Context) error {
	// Un premier passage immédiat : au redémarrage, il reste souvent ce que le
	// processus précédent n'a pas eu le temps de nettoyer.
	p.once(ctx)

	ticker := time.NewTicker(purgeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			p.once(ctx)
		}
	}
}

func (p *Purger) once(ctx context.Context) {
	if sessions, err := p.sessions.PurgeExpired(ctx); err != nil {
		p.logger.Error("purge des sessions", "error", err)
	} else if sessions > 0 {
		p.logger.Info("sessions échues supprimées", "count", sessions)
	}

	if flows, err := p.flows.PurgeExpiredFlows(ctx); err != nil {
		p.logger.Error("purge des transactions d'authentification", "error", err)
	} else if flows > 0 {
		p.logger.Info("transactions d'authentification abandonnées supprimées", "count", flows)
	}
}
