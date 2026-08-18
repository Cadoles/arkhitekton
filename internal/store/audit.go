package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// AuditEntry est une ligne du journal d'audit.
type AuditEntry struct {
	ID     int64
	Actor  string
	Action string
	Target string
	// Payload porte ce qui distingue deux occurrences de la même action : le tag
	// poussé, l'environnement visé, les paramètres d'un lancement.
	Payload map[string]any
	At      time.Time
}

// Actions consignées. Nommées ici plutôt qu'en chaînes libres à l'appel : une
// faute de frappe dans un `action` rend la ligne introuvable au moment où on la
// cherche, c'est-à-dire après coup.
const (
	AuditReleaseCreate = "release.create"
	// La validation d'une release engage ce qui partira chez l'utilisateur. Elle
	// est consignée à part de sa création : ce sont deux décisions distinctes,
	// souvent prises par des personnes différentes et à plusieurs jours d'écart.
	AuditReleaseRelease   = "release.release"
	AuditReleasePromote   = "release.promote"
	AuditReleaseActions   = "release.actions"
	AuditJobEnqueue       = "job.enqueue"
	AuditJobCancel        = "job.cancel"
	AuditArtifactGenerate = "artifact.generate"
	AuditBaselinePin      = "bench.baseline.pin"
	// La validation de recette est la décision qui autorise une livraison. Son
	// historique ne peut pas vivre dans `release_signoffs`, où un changement
	// d'avis remplace le précédent : le journal est le seul endroit qui répond à
	// « qui avait validé, avant de se rétracter ».
	AuditSignoffSet   = "release.signoff"
	AuditSignoffClear = "release.signoff.clear"
	// Les changements de rôle sont consignés comme les actions sortantes : ils
	// décident de qui peut livrer, et « qui a promu qui » doit avoir une réponse
	// sans consulter les journaux applicatifs.
	AuditRoleGrant  = "role.grant"
	AuditRoleRevoke = "role.revoke"
)

// Audit consigne une action.
//
// La table est posée par la migration 0004 : « qui a promu quoi, et quand » doit
// avoir une réponse sans consulter les journaux applicatifs.
//
// L'erreur est rendue mais l'appelant n'a pas à la traiter comme un échec de son
// action : le journal est une trace, pas une transaction. Refuser une promotion
// parce que son enregistrement a échoué serait une régression de disponibilité
// pour un gain nul — la promotion, elle, a bien eu lieu.
func (db *DB) Audit(ctx context.Context, actor, action, target string, payload map[string]any) error {
	var encoded []byte
	if payload != nil {
		var err error
		encoded, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("journal d'audit %q: %w", action, err)
		}
	}

	_, err := db.pool.Exec(ctx,
		`insert into audit_log (actor, action, target, payload) values ($1, $2, $3, $4)`,
		actor, action, nullIfEmpty(target), encoded)
	if err != nil {
		return fmt.Errorf("journal d'audit %q: %w", action, err)
	}
	return nil
}

// ListAudit rend le journal, du plus récent au plus ancien.
//
// `target` vide rend tout le journal ; renseigné, il rend l'historique d'un objet
// — les deux lectures pour lesquelles les index existent.
func (db *DB) ListAudit(ctx context.Context, target string, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	rows, err := db.pool.Query(ctx,
		`select id, actor, action, coalesce(target, ''), payload, at
		 from audit_log
		 where $1 = '' or target = $1
		 order by at desc, id desc
		 limit $2`, target, limit)
	if err != nil {
		return nil, fmt.Errorf("lecture du journal d'audit: %w", err)
	}
	defer rows.Close()

	var entries []AuditEntry
	for rows.Next() {
		var entry AuditEntry
		var payload []byte
		if err := rows.Scan(&entry.ID, &entry.Actor, &entry.Action,
			&entry.Target, &payload, &entry.At); err != nil {
			return nil, fmt.Errorf("lecture du journal d'audit: %w", err)
		}
		if len(payload) > 0 {
			if err := json.Unmarshal(payload, &entry.Payload); err != nil {
				return nil, fmt.Errorf("lecture du journal d'audit: %w", err)
			}
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
