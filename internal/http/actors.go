package http

import (
	"context"
	"log/slog"
)

/*
 * Nom d'un acteur, pour l'affichage.
 *
 * Les traces enregistrent le sujet OIDC — `121` — parce qu'il est stable. Le
 * lecteur, lui, cherche « qui ». La traduction se fait ici, à la lecture : les
 * traces déjà écrites redeviennent lisibles sans migration, et un utilisateur
 * renommé l'est partout d'un coup.
 */

// DirectoryReader est ce que la traduction attend de la base.
type DirectoryReader interface {
	Directory(ctx context.Context) (map[string]string, error)
}

// actors traduit des sujets en noms.
//
// Chargé une fois par requête et non par acteur : une liste de cinquante
// releases interrogerait sinon la base cinquante fois pour le même annuaire.
type actors struct {
	names map[string]string
}

// resolveActors charge l'annuaire.
//
// Un échec ne fait pas échouer l'écran : les sujets s'affichent alors tels
// quels, ce qui est le comportement d'avant. Perdre une page entière parce qu'un
// nom n'a pas pu être lu serait disproportionné — mais le taire aussi, d'où le
// journal.
func resolveActors(ctx context.Context, source DirectoryReader, logger *slog.Logger) actors {
	names, err := source.Directory(ctx)
	if err != nil {
		if logger != nil {
			logger.Error("annuaire des utilisateurs", "error", err)
		}
		return actors{}
	}
	return actors{names: names}
}

// name rend le nom d'un sujet, ou le sujet lui-même.
//
// Le repli sur le sujet est délibéré : un acteur inconnu de l'annuaire — compte
// de service, utilisateur supprimé, action antérieure à la table des rôles —
// doit rester identifiable. Une chaîne vide effacerait la trace de qui a agi,
// ce qui est exactement ce qu'un journal d'audit ne doit jamais faire.
func (a actors) name(subject string) string {
	if name, ok := a.names[subject]; ok && name != "" {
		return name
	}
	return subject
}
