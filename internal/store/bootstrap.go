package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

/*
 * Amorçage des données.
 *
 * Ce qui est posé une fois, à la migration du schéma, puis géré en base.
 *
 * Deux jeux de données naissaient d'une variable d'environnement relue à chaque
 * fois : les administrateurs, testés à chaque connexion, et les dépôts pilotés,
 * redéclarés à chaque démarrage. Relire indéfiniment une configuration
 * d'amorçage a deux défauts.
 *
 * La base cesse d'être la source : retirer un dépôt de l'interface le voyait
 * revenir au redémarrage suivant, sans que rien à l'écran ne l'explique. Et la
 * configuration devient un pouvoir permanent — qui peut l'éditer peut se
 * redonner un rôle administrateur, indéfiniment, sans laisser d'autre trace
 * qu'une connexion.
 */

// Étapes d'amorçage. Nommées ici plutôt qu'en chaînes libres à l'appel : une
// faute de frappe rejouerait une étape déjà appliquée, ce qui est précisément ce
// que la table existe pour empêcher.
const (
	BootstrapGrants   = "grants"
	BootstrapProjects = "projects"
)

// Grant est un rôle déclaré avant que le sujet de son titulaire ne soit connu.
type Grant struct {
	// Identifier est le `subject` si on le connaît, le nom de compte sinon.
	Identifier string
	Role       string
}

// BootstrapSpec porte ce qu'un amorçage a à poser.
type BootstrapSpec struct {
	Grants   []Grant
	Projects []Project
}

// BootstrapReport dit ce que l'amorçage a fait, étape par étape.
//
// Rendu à l'appelant plutôt que journalisé ici : c'est lui qui sait à quel
// niveau le dire, et une étape sautée n'est pas un événement de même poids
// qu'une étape appliquée.
type BootstrapReport struct {
	GrantsApplied   int
	ProjectsApplied int
	Skipped         []string
}

// Bootstrap applique les étapes qui ne l'ont pas encore été.
//
// Une étape déjà présente dans `bootstrap_steps` n'est **jamais** rejouée, même
// si la configuration a changé depuis. C'est ce qui rend la base seule maîtresse
// après le premier passage : un dépôt retiré de l'interface ne revient pas, et un
// rôle retiré à quelqu'un ne se réattribue pas à la faveur d'un redémarrage.
//
// Chaque étape est appliquée dans sa propre transaction, avec sa trace : une
// étape qui échoue n'empêche pas la suivante d'aboutir, et ne se retrouve pas
// marquée comme faite.
func (db *DB) Bootstrap(ctx context.Context, spec BootstrapSpec) (BootstrapReport, error) {
	var report BootstrapReport
	var problems []error

	applied, count, err := db.applyStep(ctx, BootstrapGrants, func(ctx context.Context) (int, string, error) {
		return db.seedGrants(ctx, spec.Grants)
	})
	switch {
	case err != nil:
		problems = append(problems, err)
	case applied:
		report.GrantsApplied = count
	default:
		report.Skipped = append(report.Skipped, BootstrapGrants)
	}

	applied, count, err = db.applyStep(ctx, BootstrapProjects, func(ctx context.Context) (int, string, error) {
		declared, err := db.SeedProjects(ctx, spec.Projects)
		return declared, fmt.Sprintf("%d dépôt(s)", declared), err
	})
	switch {
	case err != nil:
		problems = append(problems, err)
	case applied:
		report.ProjectsApplied = count
	default:
		report.Skipped = append(report.Skipped, BootstrapProjects)
	}

	return report, errors.Join(problems...)
}

// applyStep exécute une étape si elle n'a pas déjà été appliquée.
//
// Le marquage est fait **dans la même transaction** que l'étape : les séparer
// laisserait une fenêtre où l'étape a eu lieu sans être marquée — et un second
// démarrage la rejouerait, ce qui est exactement ce qu'on veut éviter.
//
// La réservation passe par `insert … on conflict do nothing` plutôt que par une
// lecture suivie d'une écriture : deux Jobs de migration lancés en parallèle —
// ce que Kubernetes fait volontiers après un échec — ne doivent pas appliquer
// deux fois la même étape.
func (db *DB) applyStep(
	ctx context.Context, step string, apply func(context.Context) (int, string, error),
) (bool, int, error) {
	transaction, err := db.pool.Begin(ctx)
	if err != nil {
		return false, 0, fmt.Errorf("amorçage %q: %w", step, err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	tag, err := transaction.Exec(ctx,
		`insert into bootstrap_steps (step) values ($1) on conflict (step) do nothing`, step)
	if err != nil {
		return false, 0, fmt.Errorf("amorçage %q: %w", step, err)
	}
	if tag.RowsAffected() == 0 {
		return false, 0, nil
	}

	count, detail, err := apply(ctx)
	if err != nil {
		return false, 0, fmt.Errorf("amorçage %q: %w", step, err)
	}

	if _, err := transaction.Exec(ctx,
		`update bootstrap_steps set detail = $2 where step = $1`, step, detail); err != nil {
		return false, 0, fmt.Errorf("amorçage %q: %w", step, err)
	}

	if err := transaction.Commit(ctx); err != nil {
		return false, 0, fmt.Errorf("amorçage %q: %w", step, err)
	}
	return true, count, nil
}

// seedGrants déclare les rôles en attente d'un sujet.
func (db *DB) seedGrants(ctx context.Context, grants []Grant) (int, string, error) {
	declared := 0
	for _, grant := range grants {
		identifier := strings.TrimSpace(grant.Identifier)
		if identifier == "" || grant.Role == "" {
			continue
		}

		tag, err := db.pool.Exec(ctx,
			`insert into bootstrap_grants (identifier, role) values ($1, $2)
			 on conflict (identifier) do nothing`, identifier, grant.Role)
		if err != nil {
			return declared, "", fmt.Errorf("rôle déclaré pour %q: %w", identifier, err)
		}
		if tag.RowsAffected() > 0 {
			declared++
		}
	}
	return declared, fmt.Sprintf("%d rôle(s) déclarés", declared), nil
}

// PendingGrants liste les rôles déclarés qui n'ont pas encore trouvé leur
// titulaire.
//
// Sert au diagnostic : un identifiant mal orthographié n'a aucun symptôme visible
// — l'utilisateur se connecte simplement sans permission — et cette liste est le
// seul endroit où l'écart se voit.
func (db *DB) PendingGrants(ctx context.Context) ([]Grant, error) {
	rows, err := db.pool.Query(ctx,
		`select identifier, role from bootstrap_grants
		 where claimed_at is null order by identifier`)
	if err != nil {
		return nil, fmt.Errorf("rôles déclarés en attente: %w", err)
	}
	defer rows.Close()

	var grants []Grant
	for rows.Next() {
		var grant Grant
		if err := rows.Scan(&grant.Identifier, &grant.Role); err != nil {
			return nil, fmt.Errorf("rôles déclarés en attente: %w", err)
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}
