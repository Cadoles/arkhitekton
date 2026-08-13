package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrSchemaOutdated signale un schéma en retard sur le binaire.
var ErrSchemaOutdated = errors.New("schéma de base en retard sur le binaire")

// gooseVersionTable est la table tenue par goose. Le nom est fixé par la
// bibliothèque ; l'interroger directement évite d'ouvrir un second *sql.DB à
// chaque appel de sonde, ce que ferait MigrationState.
const gooseVersionTable = "goose_db_version"

// ExpectedSchemaVersion rend le numéro de la dernière migration embarquée.
//
// Les fichiers sont nommés `NNNN_libellé.sql` ; goose retient le préfixe
// numérique comme version. Le dériver ici plutôt que de le coder en dur fait que
// l'ajout d'une migration suffit à déplacer l'attente.
func ExpectedSchemaVersion() (int64, error) {
	names, err := MigrationNames()
	if err != nil {
		return 0, err
	}
	if len(names) == 0 {
		return 0, errors.New("aucune migration embarquée")
	}

	var highest int64
	for _, name := range names {
		prefix, _, found := strings.Cut(name, "_")
		if !found {
			return 0, fmt.Errorf("migration %q: nom sans préfixe de version", name)
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("migration %q: préfixe de version illisible: %w", name, err)
		}
		if version > highest {
			highest = version
		}
	}
	return highest, nil
}

// SchemaVersion rend la version appliquée en base.
func (db *DB) SchemaVersion(ctx context.Context) (int64, error) {
	if db == nil || db.pool == nil {
		return 0, errors.New("pool non initialisé")
	}

	// coalesce couvre la table présente mais vide ; la table absente remonte en
	// erreur, ce qui est le cas « migrations jamais exécutées ».
	var applied int64
	err := db.pool.QueryRow(ctx,
		`select coalesce(max(version_id), 0) from `+gooseVersionTable+` where is_applied`,
	).Scan(&applied)
	if err != nil {
		return 0, fmt.Errorf("lecture de la version du schéma: %w", err)
	}
	return applied, nil
}

// CheckSchema alimente la sonde /readyz.
//
// Sans elle, un pod `api` démarré avant la fin du Job de migration se déclarerait
// prêt — PostgreSQL répond au ping — puis répondrait 500 sur chaque requête
// touchant une table absente. La sonde de préparation existe précisément pour
// écarter du Service une instance qui ne peut pas servir : le retard de schéma
// entre dans cette catégorie.
//
// Un schéma en avance est accepté : c'est l'état normal d'un déploiement
// expand/contract, où les migrations sont appliquées avant le nouveau code.
func (db *DB) CheckSchema(ctx context.Context) error {
	expected, err := ExpectedSchemaVersion()
	if err != nil {
		return err
	}

	applied, err := db.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if applied < expected {
		return fmt.Errorf("%w: version %d appliquée, %d attendue", ErrSchemaOutdated, applied, expected)
	}
	return nil
}
