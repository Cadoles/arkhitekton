package worker

import (
	"context"
	"fmt"
	"log/slog"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
Rejeu des résultats depuis les artefacts bruts.

Un parseur se corrige. Une métrique s'ajoute. Une famille de tests change de
format. À chaque fois, l'historique déjà projeté devient faux — et le
reconstruire en relançant les pipelines est impossible : les runs sont passés,
leur cible a bougé, et une campagne E2E de quarante minutes ne se rejoue pas pour
corriger une expression régulière.

Le rejeu part donc du **brut conservé**. Chaque rapport enregistre la clé de
l'objet dont il est issu, et cette clé est ce qui rend l'historique
reconstructible sans rien réexécuter.

Trois propriétés en découlent.

**Effacer puis reconstruire, jamais empiler.** Un rejeu qui ajoute dupliquerait
chaque cas et fausserait toute comparaison inter-release — précisément ce que le
rejeu sert à corriger.

**Un run à la fois, et l'échec de l'un n'emporte pas les autres.** Rejouer
cinquante runs et tout perdre parce que le troisième a un artefact purgé serait
une régression pire que le défaut d'origine.

**Le mode simulation est le mode par défaut du doute.** Rejouer efface avant de
reconstruire : pouvoir constater ce qui disparaîtrait, sans rien détruire, est ce
qui rend l'opération défendable.
*/

// ReprocessStore est ce que le rejeu attend de la base.
type ReprocessStore interface {
	ListReprocessTargets(ctx context.Context, releaseID int64) ([]store.ReprocessTarget, error)
	DeleteTestResults(ctx context.Context, runID int64) (int64, error)
}

// Reprocessor rejoue les résultats d'un ou plusieurs runs.
type Reprocessor struct {
	store    ReprocessStore
	tests    *TestIngester
	security *SecurityIngester
	load     *LoadIngester
	logger   *slog.Logger
}

func NewReprocessor(
	targets ReprocessStore, tests *TestIngester, security *SecurityIngester,
	load *LoadIngester, logger *slog.Logger,
) *Reprocessor {
	return &Reprocessor{store: targets, tests: tests, security: security, load: load, logger: logger}
}

// ReprocessOptions borne ce que le rejeu touche.
type ReprocessOptions struct {
	// ReleaseID limite le rejeu à une release. Zéro rejoue tout l'historique.
	ReleaseID int64
	// DryRun n'efface ni ne reconstruit : il énumère ce qui serait rejoué. Ce
	// mode existe parce que le rejeu détruit avant de reconstruire, et qu'une
	// opération destructive doit pouvoir être regardée avant d'être lancée.
	DryRun bool
}

// ReprocessReport rend compte de ce qui a été fait.
type ReprocessReport struct {
	Examined int
	Replayed int
	Deleted  int64
	// Failed nomme les runs dont le rejeu a échoué. Nommés et non comptés :
	// savoir lesquels est ce qui permet de décider s'il faut s'en inquiéter.
	Failed []string
}

// Run rejoue les résultats.
func (r *Reprocessor) Run(ctx context.Context, options ReprocessOptions) (ReprocessReport, error) {
	targets, err := r.store.ListReprocessTargets(ctx, options.ReleaseID)
	if err != nil {
		return ReprocessReport{}, err
	}

	report := ReprocessReport{Examined: len(targets)}

	for _, target := range targets {
		if options.DryRun {
			r.logger.Info("rejeu simulé",
				"run", target.RunName, "outils actuels", target.Tools)
			continue
		}

		deleted, err := r.replay(ctx, target)
		if err != nil {
			// L'échec d'un run n'emporte pas les autres : un artefact purgé ne
			// doit pas priver l'historique entier de sa reconstruction.
			r.logger.Error("rejeu", "run", target.RunName, "error", err)
			report.Failed = append(report.Failed, target.RunName)
			continue
		}

		report.Replayed++
		report.Deleted += deleted
	}

	return report, nil
}

// replay efface les résultats d'un run puis les reconstruit depuis le brut.
//
// L'effacement précède volontairement la reconstruction : les ingesteurs sont
// idempotents et refuseraient d'écrire par-dessus l'existant, ce qui rendrait le
// rejeu inopérant — l'inverse exact de ce qu'on demande.
//
// La contrepartie est réelle : entre l'effacement et la reconstruction, le run
// n'a plus de résultats. Une interruption à cet instant laisse un trou. C'est
// accepté parce que le brut, lui, reste — un second rejeu répare, alors qu'une
// projection dupliquée demanderait un nettoyage manuel.
func (r *Reprocessor) replay(ctx context.Context, target store.ReprocessTarget) (int64, error) {
	deleted, err := r.store.DeleteTestResults(ctx, target.RunID)
	if err != nil {
		return 0, err
	}

	run := LoadRun{ID: target.RunID, Name: target.RunName, ReleaseID: target.ReleaseID}

	// Les trois ingesteurs sont rejoués : un run peut porter des rapports de
	// tests, un audit et une campagne de charge. N'en rejouer qu'un
	// reconstruirait une partie de ce qui vient d'être effacé.
	var problems []error
	if r.tests != nil {
		if err := r.tests.Ingest(ctx, run); err != nil {
			problems = append(problems, err)
		}
	}
	if r.security != nil {
		if err := r.security.Ingest(ctx, run); err != nil {
			problems = append(problems, err)
		}
	}
	if r.load != nil {
		if err := r.load.Ingest(ctx, run); err != nil {
			problems = append(problems, err)
		}
	}

	if len(problems) > 0 {
		return deleted, fmt.Errorf("run %q: %w", target.RunName, problems[0])
	}

	r.logger.Info("run rejoué",
		"run", target.RunName, "rapports effacés", deleted, "outils précédents", target.Tools)
	return deleted, nil
}
