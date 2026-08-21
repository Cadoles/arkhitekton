package store

import (
	"context"
	"fmt"
)

/*
 * Annuaire : du sujet OIDC au nom qu'on lit.
 *
 * Les traces d'action — création d'une release, mise en file d'un job, ajout
 * d'une demande à une note — enregistrent le **sujet**, c'est-à-dire `121`.
 * C'est le bon choix côté stockage : le sujet est stable, un nom d'utilisateur
 * change. Mais c'est illisible à l'écran, où le lecteur cherche « qui », pas
 * « quel identifiant ».
 *
 * La résolution se fait donc à la lecture, et non en enregistrant le nom à côté
 * du sujet. Deux raisons : les traces déjà écrites redeviennent lisibles sans
 * migration, et un utilisateur renommé l'est partout d'un coup plutôt que sur
 * ses seules actions futures.
 */

// Directory associe les sujets connus à leur nom.
//
// Rendu en une fois plutôt que sujet par sujet : la table tient en quelques
// dizaines de lignes, et une lecture par acteur d'une liste de cinquante
// releases coûterait cinquante allers-retours pour le même résultat.
func (db *DB) Directory(ctx context.Context) (map[string]string, error) {
	rows, err := db.pool.Query(ctx,
		`select subject, coalesce(username, '') from user_roles where username <> ''`)
	if err != nil {
		return nil, fmt.Errorf("annuaire: %w", err)
	}
	defer rows.Close()

	names := map[string]string{}
	for rows.Next() {
		var subject, username string
		if err := rows.Scan(&subject, &username); err != nil {
			return nil, fmt.Errorf("annuaire: %w", err)
		}
		names[subject] = username
	}
	return names, rows.Err()
}
